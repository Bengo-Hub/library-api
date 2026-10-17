package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Bengo-Hub/httpware/objectstore"
	sharedpagination "github.com/Bengo-Hub/pagination"
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
	db       *ent.Client
	treasury *treasury.Client
	media    *objectstore.Store // e-book files (private keys, streamed to readers)
	log      *zap.Logger
}

// NewEbookHandler builds the ebook handler.
func NewEbookHandler(db *ent.Client, treasuryClient *treasury.Client, media *objectstore.Store, log *zap.Logger) *EbookHandler {
	return &EbookHandler{db: db, treasury: treasuryClient, media: media, log: log}
}

// UploadFile godoc
// @Summary Upload an e-book file (PDF, EPUB or MP3, up to 100 MB) to the private media store
// @Description multipart/form-data field "file". Answers the stored key; send it as file_url when creating the e-book.
// @Tags Ebooks
// @Accept multipart/form-data
// @Produce json
// @Router /{tenant}/library/ebooks/files [post]
func (h *EbookHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	if !h.media.Enabled() {
		respondError(w, http.StatusServiceUnavailable, "media storage not configured", "media_disabled")
		return
	}
	up, err := objectstore.ReadUpload(w, r, "file", objectstore.TypeEbooks)
	if err != nil {
		if objectstore.IsUploadError(err) {
			respondError(w, http.StatusBadRequest, err.Error(), "invalid_request")
			return
		}
		respondError(w, http.StatusInternalServerError, "could not read the upload", "store_failed")
		return
	}
	key, err := h.media.NewKey(objectstore.Owner{TenantID: tenantID.String()}, objectstore.TypeEbooks, up.Ext)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "could not store the file", "store_failed")
		return
	}
	if err := h.media.Put(r.Context(), key, up.Data, up.ContentType); err != nil {
		h.log.Error("store ebook failed", zap.Error(err))
		respondError(w, http.StatusBadGateway, "could not store the file", "store_failed")
		return
	}
	format := ebook.FormatPDF
	switch up.ContentType {
	case "application/epub+zip":
		format = ebook.FormatEPUB
	case "audio/mpeg":
		format = ebook.FormatAUDIO
	}
	respondJSON(w, http.StatusCreated, map[string]any{"file_url": key, "format": format, "size": len(up.Data)})
}

// List godoc
// @Summary List e-books
// @Tags Ebooks
// @Router /{tenant}/library/ebooks [get]
func (h *EbookHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	q := h.db.Ebook.Query().Where(ebook.TenantID(tenantID))
	params := sharedpagination.Parse(r)
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Limit(params.Limit).Offset(params.Offset).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, sharedpagination.NewResponse(rows, total, params))
}

type ebookRequest struct {
	BibRecordID        string `json:"bib_record_id"`
	FileURL            string `json:"file_url"`
	Format             string `json:"format"`
	LendingModel       string `json:"lending_model"`
	MaxConcurrentLoans int    `json:"max_concurrent_loans"`
	LoanDurationDays   int    `json:"loan_duration_days"`
}

// Create registers an e-book record. file_url is the key answered by POST /ebooks/files.
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
	// Only a file this tenant uploaded to the library store is accepted.
	fileKey := h.media.Normalize(req.FileURL)
	if !h.media.Owned(fileKey) || !strings.HasPrefix(fileKey, "library/"+tenantID.String()+"/"+objectstore.TypeEbooks+"/") {
		respondError(w, http.StatusBadRequest, "upload the file with POST /ebooks/files and send the key it answers", "invalid_request")
		return
	}
	c := h.db.Ebook.Create().SetTenantID(tenantID).SetBibRecordID(bibID).SetFileURL(fileKey)
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

// streamEbookFile streams an e-book's bytes from the private media store with range support (so
// the in-browser PDF/EPUB reader and the audio player can seek). Callers check the loan or
// purchase first; the file itself is never public.
func (h *EbookHandler) streamEbookFile(w http.ResponseWriter, r *http.Request, eb *ent.Ebook, attachment bool) {
	ct := "application/pdf"
	switch eb.Format {
	case ebook.FormatEPUB:
		ct = "application/epub+zip"
	case ebook.FormatAUDIO:
		ct = "audio/mpeg"
	}
	if attachment {
		w.Header().Set("Content-Disposition", `attachment; filename="ebook-`+eb.ID.String()+path.Ext(eb.FileURL)+`"`)
	}
	// Only this service's own keys are served, so a crafted file_url cannot reach another
	// service's files or an arbitrary URL.
	if !h.media.Owned(eb.FileURL) {
		respondError(w, http.StatusNotFound, "file not available", "file_missing")
		return
	}
	if err := h.media.Serve(w, r, eb.FileURL, ct); err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			respondError(w, http.StatusNotFound, "file not available", "file_missing")
			return
		}
		// Serve sets Accept-Ranges as it starts writing; without it nothing went out yet.
		if w.Header().Get("Accept-Ranges") == "" {
			h.log.Error("ebook stream failed", zap.Error(err), zap.String("ebook_id", eb.ID.String()))
			respondError(w, http.StatusBadGateway, "file not available", "file_error")
			return
		}
		h.log.Debug("ebook stream ended early", zap.Error(err), zap.String("ebook_id", eb.ID.String()))
	}
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
