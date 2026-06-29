package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/documentsequence"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

// SequenceHandler exposes the document-sequence configuration (membership_no, accession_no, …)
// so admins can tune the prefix / format / padding / reset period from Settings.
type SequenceHandler struct {
	db  *ent.Client
	log *zap.Logger
}

// NewSequenceHandler builds the sequence settings handler.
func NewSequenceHandler(db *ent.Client, log *zap.Logger) *SequenceHandler {
	return &SequenceHandler{db: db, log: log}
}

// managedKinds are the sequence kinds surfaced (and auto-created) in Settings.
var managedKinds = []struct{ kind, label, prefix string }{
	{sequence.KindMembership, "Membership number", "MBR"},
	{sequence.KindAccession, "Accession number", "ACC"},
	{sequence.KindLoan, "Loan number", "LN"},
}

type sequenceResponse struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Prefix      string `json:"prefix"`
	Format      string `json:"format"`
	PadWidth    int    `json:"pad_width"`
	ResetPeriod string `json:"reset_period"`
	NextValue   int64  `json:"next_value"`
	Preview     string `json:"preview"`
}

func toSequenceResponse(s *ent.DocumentSequence, label string) sequenceResponse {
	return sequenceResponse{
		ID: s.ID.String(), Kind: s.Kind, Label: label, Prefix: s.Prefix,
		Format: s.Format, PadWidth: s.PadWidth, ResetPeriod: s.ResetPeriod, NextValue: s.NextValue,
		Preview: sequence.Render(s.Format, s.Prefix, s.PadWidth, s.NextValue, time.Now()),
	}
}

func labelFor(kind string) string {
	for _, m := range managedKinds {
		if m.kind == kind {
			return m.label
		}
	}
	return kind
}

// List godoc
// @Router /{tenant}/library/settings/sequences [get]
// List returns the tenant's document-sequence configs, auto-creating any managed kind missing.
func (h *SequenceHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	ctx := r.Context()
	for _, m := range managedKinds {
		exists, _ := h.db.DocumentSequence.Query().
			Where(documentsequence.TenantID(tenantID), documentsequence.Kind(m.kind)).Exist(ctx)
		if !exists {
			reset := sequence.ResetNone
			if m.kind == sequence.KindMembership {
				reset = sequence.ResetYearly
			}
			_, _ = h.db.DocumentSequence.Create().
				SetTenantID(tenantID).SetKind(m.kind).SetPrefix(m.prefix).SetNextValue(1).
				SetPadWidth(5).SetFormat(sequence.DefaultFormat(m.kind)).
				SetResetPeriod(reset).Save(ctx)
		}
	}
	rows, err := h.db.DocumentSequence.Query().
		Where(documentsequence.TenantID(tenantID)).Order(ent.Asc(documentsequence.FieldKind)).All(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	out := make([]sequenceResponse, 0, len(rows))
	for _, s := range rows {
		out = append(out, toSequenceResponse(s, labelFor(s.Kind)))
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}

type sequenceUpdateRequest struct {
	Prefix      *string `json:"prefix"`
	Format      *string `json:"format"`
	PadWidth    *int    `json:"pad_width"`
	ResetPeriod *string `json:"reset_period"`
	NextValue   *int64  `json:"next_value"`
}

// Update godoc
// @Router /{tenant}/library/settings/sequences/{kind} [put]
// Update edits a sequence's prefix / format / padding / reset period (and optionally next value).
func (h *SequenceHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	kind := chi.URLParam(r, "kind")
	var req sequenceUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}
	ctx := r.Context()
	seq, err := h.db.DocumentSequence.Query().
		Where(documentsequence.TenantID(tenantID), documentsequence.Kind(kind)).Only(ctx)
	if ent.IsNotFound(err) {
		respondError(w, http.StatusNotFound, "sequence not found", "not_found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "load_failed")
		return
	}
	upd := h.db.DocumentSequence.UpdateOne(seq)
	if req.Prefix != nil {
		upd = upd.SetPrefix(*req.Prefix)
	}
	if req.Format != nil {
		upd = upd.SetFormat(*req.Format)
	}
	if req.PadWidth != nil && *req.PadWidth > 0 {
		upd = upd.SetPadWidth(*req.PadWidth)
	}
	if req.ResetPeriod != nil {
		upd = upd.SetResetPeriod(*req.ResetPeriod)
	}
	if req.NextValue != nil && *req.NextValue > 0 {
		upd = upd.SetNextValue(*req.NextValue)
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	respondJSON(w, http.StatusOK, toSequenceResponse(saved, labelFor(saved.Kind)))
}
