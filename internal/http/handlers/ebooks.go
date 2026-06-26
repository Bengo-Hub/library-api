package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/ebook"
	"github.com/bengobox/library-service/internal/ent/ebookloan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/events"
	"github.com/bengobox/library-service/internal/platform/treasury"
)

// EbookHandler serves the digital shelf, controlled-digital-lending, and (Phase 2)
// one-time purchase + secured download endpoints.
type EbookHandler struct {
	db        *ent.Client
	treasury  *treasury.Client
	ebookRoot string
	log       *zap.Logger
}

// NewEbookHandler builds the ebook handler.
func NewEbookHandler(db *ent.Client, treasuryClient *treasury.Client, ebookRoot string, log *zap.Logger) *EbookHandler {
	return &EbookHandler{db: db, treasury: treasuryClient, ebookRoot: ebookRoot, log: log}
}

// List godoc
// @Summary List e-books
// @Tags Ebooks
// @Router /{tenant}/library/ebooks [get]
func (h *EbookHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	rows, err := h.db.Ebook.Query().Where(ebook.TenantID(tenantID)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

type ebookRequest struct {
	BibRecordID        string `json:"bib_record_id"`
	FileURL            string `json:"file_url"`
	Format             string `json:"format"`
	LendingModel       string `json:"lending_model"`
	MaxConcurrentLoans int    `json:"max_concurrent_loans"`
	LoanDurationDays   int    `json:"loan_duration_days"`
}

// Create registers an e-book record (file uploaded separately to the media PVC).
// @Router /{tenant}/library/ebooks [post]
func (h *EbookHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req ebookRequest
	if err := Decode(r, &req); err != nil || req.BibRecordID == "" || req.FileURL == "" {
		respondError(w, http.StatusBadRequest, "bib_record_id and file_url are required", "invalid_request")
		return
	}
	bibID, err := uuid.Parse(req.BibRecordID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad bib_record_id", "invalid_request")
		return
	}
	c := h.db.Ebook.Create().SetTenantID(tenantID).SetBibRecordID(bibID).SetFileURL(req.FileURL)
	if req.Format != "" {
		c.SetFormat(ebook.Format(req.Format))
	}
	if req.LendingModel != "" {
		c.SetLendingModel(ebook.LendingModel(req.LendingModel))
	}
	if req.MaxConcurrentLoans > 0 {
		c.SetMaxConcurrentLoans(req.MaxConcurrentLoans)
	}
	if req.LoanDurationDays > 0 {
		c.SetLoanDurationDays(req.LoanDurationDays)
	}
	row, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

type lendResponse struct {
	LoanID      string    `json:"loan_id"`
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Lend godoc
// @Summary Borrow an e-book (controlled digital lending — concurrency-limited)
// @Tags Ebooks
// @Router /{tenant}/library/ebooks/{id}/lend [post]
func (h *EbookHandler) Lend(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	var body struct {
		MemberID string `json:"member_id"`
	}
	_ = Decode(r, &body)
	memberID, ok := h.resolveMemberID(r, tenantID, body.MemberID)
	if !ok {
		respondError(w, http.StatusBadRequest, "member_id is required (no library membership linked to your account)", "no_member")
		return
	}

	tx, err := h.db.Tx(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "tx_failed")
		return
	}
	// Lock the ebook row so the concurrency count is consistent under load.
	eb, err := tx.Ebook.Query().Where(ebook.IDEQ(id), ebook.TenantID(tenantID)).ForUpdate().Only(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusNotFound, "ebook not found", "not_found")
		return
	}
	active, _ := tx.EbookLoan.Query().Where(ebookloan.TenantID(tenantID), ebookloan.EbookID(id), ebookloan.ReturnedAtIsNil()).Count(r.Context())
	if active >= eb.MaxConcurrentLoans {
		_ = tx.Rollback()
		respondError(w, http.StatusConflict, "all digital copies are currently lent — place a hold", "cdl_limit")
		return
	}
	token := randomToken()
	expires := time.Now().Add(time.Duration(eb.LoanDurationDays) * 24 * time.Hour)
	el, err := tx.EbookLoan.Create().
		SetTenantID(tenantID).SetEbookID(id).SetMemberID(memberID).
		SetIssuedAt(time.Now()).SetExpiresAt(expires).SetAccessToken(token).
		Save(r.Context())
	if err != nil {
		_ = tx.Rollback()
		respondError(w, http.StatusInternalServerError, err.Error(), "lend_failed")
		return
	}
	_ = events.Publish(r.Context(), tx.OutboxEvent, tenantID, el.ID.String(), events.EventEbookLoaned, map[string]any{
		"ebook_loan_id": el.ID, "ebook_id": id, "member_id": memberID, "expires_at": expires,
	})
	if err := tx.Commit(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "commit_failed")
		return
	}
	respondJSON(w, http.StatusOK, lendResponse{LoanID: el.ID.String(), AccessToken: token, ExpiresAt: expires})
}

// Read godoc
// @Summary Stream the e-book file for an active reader session (token-gated)
// @Tags Ebooks
// @Router /{tenant}/library/ebooks/{id}/read [get]
func (h *EbookHandler) Read(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		respondError(w, http.StatusUnauthorized, "missing token", "unauthorized")
		return
	}
	el, err := h.db.EbookLoan.Query().
		Where(ebookloan.TenantID(tenantID), ebookloan.EbookID(id), ebookloan.AccessToken(token), ebookloan.ReturnedAtIsNil()).
		Only(r.Context())
	if err != nil || time.Now().After(el.ExpiresAt) {
		respondError(w, http.StatusForbidden, "reading session expired or invalid", "session_invalid")
		return
	}
	eb, err := h.db.Ebook.Query().Where(ebook.IDEQ(id)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "ebook not found", "not_found")
		return
	}
	// Stream the file bytes so the in-browser reader (react-pdf / epub.js) can render them.
	// A per-loan watermark (member id + timestamp) is attached as a header; a later pass can
	// burn it into the PDF/EPUB. last_read_position is surfaced via header for resume.
	watermark := el.MemberID.String() + "-" + time.Now().Format(time.RFC3339)
	w.Header().Set("X-Library-Watermark", watermark)
	w.Header().Set("X-Library-Format", string(eb.Format))
	w.Header().Set("Access-Control-Expose-Headers", "X-Library-Watermark, X-Library-Format")
	h.streamEbookFile(w, r, eb, false)
}

// SavePosition godoc
// @Summary Persist reading progress
// @Tags Ebooks
// @Router /{tenant}/library/ebooks/loans/{id}/position [post]
func (h *EbookHandler) SavePosition(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	var body struct {
		Position map[string]any `json:"position"`
	}
	if err := Decode(r, &body); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	exists, _ := h.db.EbookLoan.Query().Where(ebookloan.IDEQ(id), ebookloan.TenantID(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "loan not found", "not_found")
		return
	}
	if _, err := h.db.EbookLoan.UpdateOneID(id).SetLastReadPosition(body.Position).Save(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "save_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"saved": true})
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// streamEbookFile streams an e-book's bytes from EBOOK_ROOT with range support (so the
// in-browser PDF/EPUB reader can seek). The relative file path is sanitized to prevent
// directory traversal outside the e-book root.
func (h *EbookHandler) streamEbookFile(w http.ResponseWriter, r *http.Request, eb *ent.Ebook, attachment bool) {
	rel := filepath.Clean("/" + eb.FileURL) // leading slash + Clean strips any ../ escape
	full := filepath.Join(h.ebookRoot, rel)

	ct := "application/pdf"
	switch eb.Format {
	case ebook.FormatEPUB:
		ct = "application/epub+zip"
	case ebook.FormatAUDIO:
		ct = "audio/mpeg"
	}
	w.Header().Set("Content-Type", ct)
	if attachment {
		w.Header().Set("Content-Disposition", `attachment; filename="ebook-`+eb.ID.String()+`"`)
	}

	f, err := os.Open(full)
	if err != nil {
		respondError(w, http.StatusNotFound, "file not available", "file_missing")
		return
	}
	defer f.Close()
	info, statErr := f.Stat()
	if statErr != nil {
		respondError(w, http.StatusInternalServerError, "stat failed", "file_error")
		return
	}
	http.ServeContent(w, r, filepath.Base(full), info.ModTime(), f)
}

// resolveMemberID returns the explicit member id when supplied, else resolves the member
// linked to the current JWT user (patron self-service). Returns false when neither yields a
// member, so a desk-only user without a linked membership must supply member_id explicitly.
func (h *EbookHandler) resolveMemberID(r *http.Request, tenantID uuid.UUID, explicit string) (uuid.UUID, bool) {
	if explicit != "" {
		if id, err := uuid.Parse(explicit); err == nil {
			return id, true
		}
	}
	uid := UserIDFrom(r)
	if uid == "" {
		return uuid.Nil, false
	}
	userUUID, err := uuid.Parse(uid)
	if err != nil {
		return uuid.Nil, false
	}
	m, err := h.db.Member.Query().Where(member.TenantID(tenantID), member.UserID(userUUID)).First(r.Context())
	if err != nil {
		return uuid.Nil, false
	}
	return m.ID, true
}
