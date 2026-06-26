package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
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

// List godoc
// @Router /{tenant}/library/holds [get]
func (h *HoldHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	q := h.db.Hold.Query().Where(hold.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(hold.StatusEQ(hold.Status(s)))
	}
	if mid := r.URL.Query().Get("member_id"); mid != "" {
		if id, err := uuid.Parse(mid); err == nil {
			q = q.Where(hold.MemberID(id))
		}
	}
	rows, err := q.Order(ent.Asc(hold.FieldQueuePosition)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
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
