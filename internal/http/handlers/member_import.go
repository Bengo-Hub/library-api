package handlers

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

// importJob tracks a bulk member-import job in memory.
type importJob struct {
	ID       string        `json:"id"`
	Status   string        `json:"status"` // running / done
	Total    int           `json:"total"`
	Imported int           `json:"imported"`
	Errors   []importError `json:"errors"`
}

type importError struct {
	Row     int    `json:"row"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

var (
	importJobs   = map[string]*importJob{}
	importJobsMu sync.RWMutex
)

func setImportJob(job *importJob) {
	importJobsMu.Lock()
	importJobs[job.ID] = job
	importJobsMu.Unlock()
}

func getImportJob(id string) (*importJob, bool) {
	importJobsMu.RLock()
	defer importJobsMu.RUnlock()
	j, ok := importJobs[id]
	return j, ok
}

// ImportMembers godoc
// @Summary Bulk import members from a CSV file
// @Tags Members
// @Router /{tenant}/library/members/import [post]
func (h *MemberHandler) ImportMembers(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		respondError(w, http.StatusBadRequest, "expected multipart/form-data", "invalid_request")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		respondError(w, http.StatusBadRequest, "file field required", "invalid_request")
		return
	}
	defer file.Close()

	cr := csv.NewReader(file)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid CSV: "+err.Error(), "csv_parse_error")
		return
	}
	if len(records) < 2 {
		respondError(w, http.StatusBadRequest, "CSV must have a header row and at least one data row", "empty_csv")
		return
	}

	job := &importJob{
		ID:     uuid.NewString(),
		Status: "running",
		Total:  len(records) - 1,
	}
	setImportJob(job)
	respondJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status": "running"})

	go func() {
		log := h.log.With(zap.String("job_id", job.ID))
		header := normaliseHeader(records[0])
		col := func(row []string, name string) string {
			idx, ok2 := header[name]
			if !ok2 || idx >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[idx])
		}

		// Resolve default tier for this tenant.
		defaultTier, _ := h.db.MemberTier.Query().
			Where(membertier.TenantID(tenantID)).
			First(r.Context())

		for i, record := range records[1:] {
			rowNum := i + 2
			displayName := col(record, "display_name")
			if displayName == "" {
				displayName = col(record, "name")
			}
			if displayName == "" {
				importJobsMu.Lock()
				job.Errors = append(job.Errors, importError{Row: rowNum, Field: "display_name", Message: "required"})
				importJobsMu.Unlock()
				continue
			}

			tierID := uuid.Nil
			if defaultTier != nil {
				tierID = defaultTier.ID
			}
			if tidStr := col(record, "tier_id"); tidStr != "" {
				if id, err := uuid.Parse(tidStr); err == nil {
					tierID = id
				}
			}
			if tierID == uuid.Nil {
				importJobsMu.Lock()
				job.Errors = append(job.Errors, importError{Row: rowNum, Field: "tier_id", Message: "no tier configured"})
				importJobsMu.Unlock()
				continue
			}

			tx, err := h.db.Tx(r.Context())
			if err != nil {
				log.Warn("tx failed", zap.Int("row", rowNum), zap.Error(err))
				continue
			}
			memberNo := col(record, "membership_no")
			if memberNo == "" {
				memberNo, err = sequence.Next(r.Context(), tx, tenantID, sequence.KindMembership, "MBR", 5)
				if err != nil {
					_ = tx.Rollback()
					log.Warn("sequence failed", zap.Int("row", rowNum), zap.Error(err))
					continue
				}
			}
			c := tx.Member.Create().
				SetTenantID(tenantID).
				SetMembershipNo(memberNo).
				SetTierID(tierID).
				SetDisplayName(displayName).
				SetContactEmail(col(record, "email")).
				SetContactPhone(col(record, "phone")).
				SetJoinedAt(time.Now())
			if st := strings.ToUpper(col(record, "status")); st != "" {
				c.SetStatus(member.Status(st))
			}
			if _, saveErr := c.Save(r.Context()); saveErr != nil {
				_ = tx.Rollback()
				importJobsMu.Lock()
				job.Errors = append(job.Errors, importError{Row: rowNum, Field: "-", Message: saveErr.Error()})
				importJobsMu.Unlock()
				continue
			}
			if commitErr := tx.Commit(); commitErr != nil {
				importJobsMu.Lock()
				job.Errors = append(job.Errors, importError{Row: rowNum, Field: "-", Message: "commit: " + commitErr.Error()})
				importJobsMu.Unlock()
				continue
			}
			importJobsMu.Lock()
			job.Imported++
			importJobsMu.Unlock()
		}

		importJobsMu.Lock()
		job.Status = "done"
		importJobsMu.Unlock()
		log.Info("member import done", zap.Int("imported", job.Imported), zap.Int("errors", len(job.Errors)))
	}()
}

// ImportMembersStatus godoc
// @Summary Poll member import job status
// @Tags Members
// @Router /{tenant}/library/members/import/{job_id} [get]
func (h *MemberHandler) ImportMembersStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "job_id")
	job, ok := getImportJob(jobID)
	if !ok {
		respondError(w, http.StatusNotFound, "job not found", "not_found")
		return
	}
	importJobsMu.RLock()
	snap := *job
	importJobsMu.RUnlock()
	respondJSON(w, http.StatusOK, snap)
}

// ImportMembersTemplate godoc
// @Summary Download a blank member import CSV template
// @Tags Members
// @Router /{tenant}/library/members/import/template [get]
func (h *MemberHandler) ImportMembersTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="members_import_template.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"display_name", "email", "phone", "membership_no", "tier_id", "status"})
	_ = cw.Write([]string{"Jane Doe", "jane@example.com", "+254700000000", "", "", "ACTIVE"})
	cw.Flush()
}

// ImportMembersErrors godoc
// @Summary Download error rows from an import job as CSV
// @Tags Members
// @Router /{tenant}/library/members/import/{job_id}/errors [get]
func (h *MemberHandler) ImportMembersErrors(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "job_id")
	job, ok := getImportJob(jobID)
	if !ok {
		respondError(w, http.StatusNotFound, "job not found", "not_found")
		return
	}
	importJobsMu.RLock()
	errs := append([]importError(nil), job.Errors...)
	importJobsMu.RUnlock()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="import_errors.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"row", "field", "message"})
	for _, e := range errs {
		_ = cw.Write([]string{strconv.Itoa(e.Row), e.Field, e.Message})
	}
	cw.Flush()
}

// normaliseHeader maps CSV header names (lowercased, trimmed) to column indices.
func normaliseHeader(row []string) map[string]int {
	m := make(map[string]int, len(row))
	for i, h := range row {
		m[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return m
}

