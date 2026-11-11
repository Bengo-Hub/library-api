package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/tenant"
)

// CRMLinksHandler tells marketflow whether the library still links a CRM contact, so marketflow
// can decide between deleting an erased person's contact and stripping it to a minimal record.
type CRMLinksHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewCRMLinksHandler builds the handler.
func NewCRMLinksHandler(db *ent.Client, log *zap.Logger) *CRMLinksHandler {
	if log == nil {
		log = zap.NewNop()
	}
	return &CRMLinksHandler{db: db, log: log}
}

// Referenced answers whether any member, in any tenant, carries the contact id. {tenant} is the
// tenant UUID or slug and must be known here. The answer is the flag only: no member numbers, names or ids.
// GET /api/v1/s2s/{tenant}/crm-links/{contactID} (X-API-Key) -> {"referenced": bool}
func (h *CRMLinksHandler) Referenced(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	contactID, err := uuid.Parse(chi.URLParam(r, "contactID"))
	if err != nil || contactID == uuid.Nil {
		respondError(w, http.StatusBadRequest, "contact id must be a UUID", "invalid_contact_id")
		return
	}
	// The tenant must be known here, by UUID or slug. An unknown UUID (a tenant this service holds
	// under another id after a sync drift) answers 404, never "not referenced": marketflow then
	// keeps the contact instead of deleting it.
	raw := chi.URLParam(r, "tenant")
	pred := tenant.Slug(raw)
	if id, perr := uuid.Parse(raw); perr == nil {
		pred = tenant.ID(id)
	}
	t, err := h.db.Tenant.Query().Where(pred).Only(ctx)
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "tenant not found", "tenant_not_found")
		return
	}
	if err != nil {
		h.log.Error("crm links: tenant lookup failed", zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to check links", "internal_error")
		return
	}
	tenantID := t.ID
	// The contact id is matched in every tenant: CRM contact ids are random UUIDs minted by
	// marketflow, so a match anywhere is this person, and a tenant mismatch can only answer
	// "referenced", which keeps the contact.
	found, err := h.db.Member.Query().Where(member.CrmContactID(contactID)).Exist(ctx)
	if err != nil {
		// Ids only in the log, never the person's details.
		h.log.Error("crm links check failed", zap.String("tenant_id", tenantID.String()),
			zap.String("contact_id", contactID.String()), zap.Error(err))
		respondError(w, http.StatusInternalServerError, "failed to check links", "internal_error")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"referenced": found})
}
