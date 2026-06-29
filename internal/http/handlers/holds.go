package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/hold"
	"github.com/bengobox/library-service/internal/ent/member"
)

// HoldHandler serves the holds/reservations endpoints.
type HoldHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewHoldHandler builds the hold handler.
func NewHoldHandler(db *ent.Client, log *zap.Logger) *HoldHandler {
	return &HoldHandler{db: db, log: log}
}

// holdStatusToWire maps the stored enum to the library-ui HoldStatus contract (WAITING → pending).
func holdStatusToWire(s hold.Status) string {
	if s == hold.StatusWAITING {
		return "pending"
	}
	return strings.ToLower(string(s))
}

// holdStatusFromWire maps a UI status filter back to the stored enum (pending → WAITING).
func holdStatusFromWire(s string) hold.Status {
	if strings.EqualFold(s, "pending") {
		return hold.StatusWAITING
	}
	return hold.Status(strings.ToUpper(s))
}

type holdResponse struct {
	ID            string  `json:"id"`
	BibRecordID   string  `json:"bib_record_id"`
	BibTitle      string  `json:"bib_title,omitempty"`
	MemberID      string  `json:"member_id"`
	MemberName    string  `json:"member_name,omitempty"`
	MembershipNo  string  `json:"membership_no,omitempty"`
	BranchID      string  `json:"branch_id,omitempty"`
	QueuePosition int     `json:"queue_position"`
	Status        string  `json:"status"`
	PlacedAt      string  `json:"placed_at,omitempty"`
	ReadyAt       *string `json:"ready_at,omitempty"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
}

// List godoc
// @Router /{tenant}/library/holds [get]
func (h *HoldHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	q := h.db.Hold.Query().Where(hold.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(hold.StatusEQ(holdStatusFromWire(s)))
	}
	if mid := r.URL.Query().Get("member_id"); mid != "" {
		if id, err := uuid.Parse(mid); err == nil {
			q = q.Where(hold.MemberID(id))
		}
	}
	rows, err := q.Order(ent.Asc(hold.FieldQueuePosition)).All(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	// Batch-resolve bib titles + member names/numbers.
	bibIDs := make([]uuid.UUID, 0, len(rows))
	memberIDs := make([]uuid.UUID, 0, len(rows))
	for _, hd := range rows {
		bibIDs = append(bibIDs, hd.BibRecordID)
		memberIDs = append(memberIDs, hd.MemberID)
	}
	titles := map[uuid.UUID]string{}
	if len(bibIDs) > 0 {
		bibs, _ := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID), bibrecord.IDIn(bibIDs...)).All(ctx)
		for _, b := range bibs {
			titles[b.ID] = b.Title
		}
	}
	type minfo struct{ name, no string }
	members := map[uuid.UUID]minfo{}
	if len(memberIDs) > 0 {
		ms, _ := h.db.Member.Query().Where(member.TenantID(tenantID), member.IDIn(memberIDs...)).All(ctx)
		for _, m := range ms {
			members[m.ID] = minfo{name: m.DisplayName, no: m.MembershipNo}
		}
	}
	out := make([]holdResponse, 0, len(rows))
	for _, hd := range rows {
		resp := holdResponse{
			ID: hd.ID.String(), BibRecordID: hd.BibRecordID.String(), BibTitle: titles[hd.BibRecordID],
			MemberID: hd.MemberID.String(), MemberName: members[hd.MemberID].name, MembershipNo: members[hd.MemberID].no,
			BranchID: hd.BranchID.String(), QueuePosition: hd.QueuePosition, Status: holdStatusToWire(hd.Status),
			PlacedAt: hd.PlacedAt.Format(time.RFC3339),
		}
		if hd.ReadyAt != nil {
			s := hd.ReadyAt.Format(time.RFC3339)
			resp.ReadyAt = &s
		}
		if hd.ExpiresAt != nil {
			s := hd.ExpiresAt.Format(time.RFC3339)
			resp.ExpiresAt = &s
		}
		out = append(out, resp)
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

type holdRequest struct {
	BibRecordID string `json:"bib_record_id"`
	MemberID    string `json:"member_id"`
	BranchID    string `json:"branch_id"`
}

// Place godoc
// @Summary Place a hold on a bib record
// @Tags Holds
// @Router /{tenant}/library/holds [post]
func (h *HoldHandler) Place(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req holdRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}
	bibID, err := uuid.Parse(req.BibRecordID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bib_record_id is required", "invalid_request")
		return
	}
	memberID, err := uuid.Parse(req.MemberID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "member_id is required", "invalid_request")
		return
	}
	m, err := h.db.Member.Query().Where(member.IDEQ(memberID), member.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		respondError(w, http.StatusNotFound, "member not found", "not_found")
		return
	}
	// Resolve branch: explicit → member home → first available copy's branch.
	branchID := uuid.Nil
	if req.BranchID != "" {
		branchID, _ = uuid.Parse(req.BranchID)
	} else if m.HomeBranchID != nil {
		branchID = *m.HomeBranchID
	} else if c, cerr := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.BibRecordID(bibID)).First(r.Context()); cerr == nil {
		branchID = c.BranchID
	}
	pos, _ := h.db.Hold.Query().Where(hold.TenantID(tenantID), hold.BibRecordID(bibID), hold.StatusEQ(hold.StatusWAITING)).Count(r.Context())
	row, err := h.db.Hold.Create().
		SetTenantID(tenantID).SetBibRecordID(bibID).SetMemberID(memberID).SetBranchID(branchID).
		SetQueuePosition(pos + 1).SetPlacedAt(time.Now()).Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	respondJSON(w, http.StatusCreated, row)
}

// Cancel godoc
// @Router /{tenant}/library/holds/{id} [delete]
func (h *HoldHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.Hold.Query().Where(hold.IDEQ(id), hold.TenantID(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if _, err := h.db.Hold.UpdateOneID(id).SetStatus(hold.StatusCANCELLED).Save(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "cancel_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

// MarkReady moves a WAITING hold to READY (shelf hold), stamping ready_at + a pickup deadline so
// the desk can notify the patron. Idempotent-ish: only WAITING holds can be marked ready.
// @Router /{tenant}/library/holds/{id}/ready [post]
func (h *HoldHandler) MarkReady(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	hd, err := h.db.Hold.Query().Where(hold.IDEQ(id), hold.TenantID(tenantID)).Only(r.Context())
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "load_failed")
		return
	}
	if hd.Status != hold.StatusWAITING {
		respondError(w, http.StatusConflict, "only a pending hold can be marked ready", "invalid_state")
		return
	}
	now := time.Now()
	if _, err := h.db.Hold.UpdateOneID(id).
		SetStatus(hold.StatusREADY).SetReadyAt(now).SetExpiresAt(now.AddDate(0, 0, 3)).Save(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ready": true})
}
