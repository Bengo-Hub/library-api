package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent/membernotificationpref"
)

// Known event types for which we surface preferences in the UI.
var knownLibraryEvents = []string{
	"loan.overdue",
	"loan.recalled",
	"hold.ready",
	"hold.expired",
	"fine.assessed",
	"fine.paid",
	"membership.fee_due",
	"ebook.expired",
	"member.expired",
	"member.graduated",
}

type notifPrefItem struct {
	EventType string `json:"event_type"`
	Channel   string `json:"channel"`
	IsEnabled bool   `json:"is_enabled"`
}

// GetNotificationPrefs returns all notification preferences for a member.
// For event×channel pairs without a row the default is is_enabled=true (opt-in by default).
func (h *MemberHandler) GetNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	memberID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid member id", "invalid_id")
		return
	}

	prefs, err := h.db.MemberNotificationPref.Query().
		Where(
			membernotificationpref.TenantIDEQ(tenantID),
			membernotificationpref.MemberIDEQ(memberID),
		).All(r.Context())
	if err != nil {
		h.log.Sugar().Warnf("notification prefs query: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to load prefs", "internal")
		return
	}

	// Index stored prefs by event+channel for fast lookup.
	stored := map[string]map[string]bool{}
	for _, p := range prefs {
		if stored[p.EventType] == nil {
			stored[p.EventType] = map[string]bool{}
		}
		stored[p.EventType][string(p.Channel)] = p.IsEnabled
	}

	channels := []string{"EMAIL", "SMS", "PUSH"}
	out := make([]notifPrefItem, 0, len(knownLibraryEvents)*len(channels))
	for _, evt := range knownLibraryEvents {
		for _, ch := range channels {
			enabled := true // opt-in default
			if byEvent, ok := stored[evt]; ok {
				if v, ok2 := byEvent[ch]; ok2 {
					enabled = v
				}
			}
			out = append(out, notifPrefItem{EventType: evt, Channel: ch, IsEnabled: enabled})
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{"data": out})
}

// UpdateNotificationPrefs upserts notification preferences for a member.
// Body: [{event_type, channel, is_enabled}]
func (h *MemberHandler) UpdateNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "tenant required", "tenant_required")
		return
	}
	memberID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid member id", "invalid_id")
		return
	}

	var items []notifPrefItem
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		respondError(w, http.StatusBadRequest, "invalid body", "bad_request")
		return
	}

	ctx := r.Context()
	validChannels := map[string]membernotificationpref.Channel{
		"EMAIL":    membernotificationpref.ChannelEMAIL,
		"SMS":      membernotificationpref.ChannelSMS,
		"WHATSAPP": membernotificationpref.ChannelWHATSAPP,
		"PUSH":     membernotificationpref.ChannelPUSH,
		"NONE":     membernotificationpref.ChannelNONE,
	}

	for _, item := range items {
		ch, valid := validChannels[item.Channel]
		if !valid || item.EventType == "" {
			continue
		}
		// Delete existing row then re-insert (simple upsert without ON CONFLICT clause in ent).
		_, _ = h.db.MemberNotificationPref.Delete().
			Where(
				membernotificationpref.TenantIDEQ(tenantID),
				membernotificationpref.MemberIDEQ(memberID),
				membernotificationpref.EventTypeEQ(item.EventType),
				membernotificationpref.ChannelEQ(ch),
			).Exec(ctx)
		_, _ = h.db.MemberNotificationPref.Create().
			SetTenantID(tenantID).
			SetMemberID(memberID).
			SetEventType(item.EventType).
			SetChannel(ch).
			SetIsEnabled(item.IsEnabled).
			Save(ctx)
	}

	respondJSON(w, http.StatusOK, map[string]any{"message": "preferences saved"})
}
