package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/libraryholiday"
	"github.com/bengobox/library-service/internal/modules/calendar"
)

// HolidayHandler serves library holiday admin endpoints.
type HolidayHandler struct {
	db   *ent.Client
	calc *calendar.Calculator
	log  *zap.Logger
}

// NewHolidayHandler builds the handler.
func NewHolidayHandler(db *ent.Client, calc *calendar.Calculator, log *zap.Logger) *HolidayHandler {
	return &HolidayHandler{db: db, calc: calc, log: log}
}

type holidayRequest struct {
	BranchID    *string `json:"branch_id"`
	HolidayDate string  `json:"holiday_date"` // YYYY-MM-DD
	Description string  `json:"description"`
	IsRecurring bool    `json:"is_recurring"`
}

// List returns holidays for the tenant, filterable by branch_id and year.
// GET /admin/holidays?branch_id=<uuid>&year=<int>
func (h *HolidayHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	q := h.db.LibraryHoliday.Query().Where(libraryholiday.TenantIDEQ(tenantID))

	if bid := r.URL.Query().Get("branch_id"); bid != "" {
		if id, err := uuid.Parse(bid); err == nil {
			q = q.Where(libraryholiday.Or(
				libraryholiday.BranchIDIsNil(),
				libraryholiday.BranchIDEQ(id),
			))
		}
	}
	if yr := r.URL.Query().Get("year"); yr != "" {
		if y, err := strconv.Atoi(yr); err == nil {
			start := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
			end := time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC)
			q = q.Where(libraryholiday.HolidayDateGTE(start), libraryholiday.HolidayDateLT(end))
		}
	}

	rows, err := q.Order(ent.Asc(libraryholiday.FieldHolidayDate)).All(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "list_failed")
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// Create inserts a new holiday.
// POST /admin/holidays
func (h *HolidayHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	var req holidayRequest
	if err := Decode(r, &req); err != nil || req.HolidayDate == "" {
		respondError(w, http.StatusBadRequest, "holiday_date (YYYY-MM-DD) is required", "invalid_request")
		return
	}
	date, err := time.Parse("2006-01-02", req.HolidayDate)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid holiday_date format — use YYYY-MM-DD", "invalid_request")
		return
	}

	c := h.db.LibraryHoliday.Create().
		SetTenantID(tenantID).
		SetHolidayDate(date).
		SetDescription(req.Description).
		SetIsRecurring(req.IsRecurring)
	if req.BranchID != nil && *req.BranchID != "" {
		if id, err2 := uuid.Parse(*req.BranchID); err2 == nil {
			c.SetBranchID(id)
		}
	}
	row, err := c.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "create_failed")
		return
	}
	h.calc.InvalidateHolidayCache(r.Context(), tenantID)
	respondJSON(w, http.StatusCreated, row)
}

// Update modifies an existing holiday.
// PUT /admin/holidays/{id}
func (h *HolidayHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.LibraryHoliday.Query().Where(libraryholiday.IDEQ(id), libraryholiday.TenantIDEQ(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	var req holidayRequest
	if err := Decode(r, &req); err != nil {
		respondError(w, http.StatusBadRequest, "bad body", "invalid_request")
		return
	}

	u := h.db.LibraryHoliday.UpdateOneID(id).
		SetDescription(req.Description).
		SetIsRecurring(req.IsRecurring)
	if req.HolidayDate != "" {
		if date, err2 := time.Parse("2006-01-02", req.HolidayDate); err2 == nil {
			u.SetHolidayDate(date)
		}
	}
	if req.BranchID != nil {
		if *req.BranchID == "" {
			u.ClearBranchID()
		} else if id2, err2 := uuid.Parse(*req.BranchID); err2 == nil {
			u.SetBranchID(id2)
		}
	}
	row, err := u.Save(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "update_failed")
		return
	}
	h.calc.InvalidateHolidayCache(r.Context(), tenantID)
	respondJSON(w, http.StatusOK, row)
}

// Delete removes a holiday.
// DELETE /admin/holidays/{id}
func (h *HolidayHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	id, err := ParseUUIDParam(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad id", "invalid_request")
		return
	}
	exists, _ := h.db.LibraryHoliday.Query().Where(libraryholiday.IDEQ(id), libraryholiday.TenantIDEQ(tenantID)).Exist(r.Context())
	if !exists {
		respondError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if err := h.db.LibraryHoliday.DeleteOneID(id).Exec(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), "delete_failed")
		return
	}
	h.calc.InvalidateHolidayCache(r.Context(), tenantID)
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
