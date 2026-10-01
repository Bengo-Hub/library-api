package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/redis/go-redis/v9"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/membertier"
	"github.com/bengobox/library-service/internal/modules/sequence"
)

// importJob is a bulk member-import job's progress, as reported to the polling UI.
type importJob struct {
	ID       string        `json:"id"`
	Status   string        `json:"status"` // running / done
	Total    int           `json:"total"`
	Imported int           `json:"imported"`
	Errors   []importError `json:"errors"`
	// ErrorCount is the total number of failed rows; Errors keeps at most maxStoredImportErrors.
	ErrorCount int `json:"error_count"`
}

type importError struct {
	Row     int    `json:"row"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

const (
	importJobTTL          = 24 * time.Hour
	maxStoredImportErrors = 5000
	importSaveEvery       = 50
	importJobTimeout      = 30 * time.Minute
)

// Import job snapshots live in Redis so the status and errors endpoints work on whichever
// replica the poll lands on (they were in a per-pod map: a poll routed to another pod got 404,
// and the map never shrank). Without Redis a bounded per-pod cache is used.
var (
	importJobRedis redis.UniversalClient
	importJobLocal = sharedcache.NewLocal[string, []byte](200, importJobTTL)
)

// SetImportJobRedis wires the shared store for import job snapshots (call once at startup).
func SetImportJobRedis(rdb *redis.Client) {
	if rdb == nil {
		importJobRedis = nil
		return
	}
	importJobRedis = rdb
}

func importJobKey(tenantID uuid.UUID, id string) string {
	return "library:member-import:" + tenantID.String() + ":" + id
}

func saveImportJob(ctx context.Context, tenantID uuid.UUID, job importJob) {
	b, err := json.Marshal(job)
	if err != nil {
		return
	}
	key := importJobKey(tenantID, job.ID)
	if importJobRedis != nil {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		err = importJobRedis.Set(cctx, key, b, importJobTTL).Err()
		cancel()
		if err == nil {
			return
		}
	}
	importJobLocal.Set(key, b)
}

func loadImportJob(ctx context.Context, tenantID uuid.UUID, id string) (importJob, bool) {
	var job importJob
	key := importJobKey(tenantID, id)
	var raw []byte
	if importJobRedis != nil {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		raw, _ = importJobRedis.Get(cctx, key).Bytes()
		cancel()
	}
	if raw == nil {
		raw, _ = importJobLocal.Get(key)
	}
	if raw == nil || json.Unmarshal(raw, &job) != nil {
		return job, false
	}
	return job, true
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

	job := importJob{
		ID:     uuid.NewString(),
		Status: "running",
		Total:  len(records) - 1,
	}
	saveImportJob(r.Context(), tenantID, job)
	respondJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status": "running"})

	// The request context is cancelled as soon as this handler returns the 202, which made
	// every database call in the import fail; the import runs on a detached, bounded context.
	// The goroutine owns job outright (snapshots are copies), so there is nothing to lock.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), importJobTimeout)
	go func() {
		defer cancel()
		log := h.log.With(zap.String("job_id", job.ID))
		addErr := func(e importError) {
			job.ErrorCount++
			if len(job.Errors) < maxStoredImportErrors {
				job.Errors = append(job.Errors, e)
			}
		}
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
			First(ctx)

		for i, record := range records[1:] {
			rowNum := i + 2
			displayName := col(record, "display_name")
			if displayName == "" {
				displayName = col(record, "name")
			}
			if displayName == "" {
				addErr(importError{Row: rowNum, Field: "display_name", Message: "required"})
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
				addErr(importError{Row: rowNum, Field: "tier_id", Message: "no tier configured"})
				continue
			}

			tx, err := h.db.Tx(ctx)
			if err != nil {
				log.Warn("tx failed", zap.Int("row", rowNum), zap.Error(err))
				continue
			}
			memberNo := col(record, "membership_no")
			if memberNo == "" {
				memberNo, err = sequence.Next(ctx, tx, tenantID, sequence.KindMembership, "MBR", 5)
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
			if _, saveErr := c.Save(ctx); saveErr != nil {
				_ = tx.Rollback()
				addErr(importError{Row: rowNum, Field: "-", Message: saveErr.Error()})
				continue
			}
			if commitErr := tx.Commit(); commitErr != nil {
				addErr(importError{Row: rowNum, Field: "-", Message: "commit: " + commitErr.Error()})
				continue
			}
			job.Imported++
			if (i+1)%importSaveEvery == 0 {
				saveImportJob(ctx, tenantID, job)
			}
		}

		job.Status = "done"
		saveImportJob(ctx, tenantID, job)
		log.Info("member import done", zap.Int("imported", job.Imported), zap.Int("errors", job.ErrorCount))
	}()
}

// ImportMembersStatus godoc
// @Summary Poll member import job status
// @Tags Members
// @Router /{tenant}/library/members/import/{job_id} [get]
func (h *MemberHandler) ImportMembersStatus(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	job, ok := loadImportJob(r.Context(), tenantID, chi.URLParam(r, "job_id"))
	if !ok {
		respondError(w, http.StatusNotFound, "job not found", "not_found")
		return
	}
	respondJSON(w, http.StatusOK, job)
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
	tenantID, ok := TenantUUID(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "missing tenant", "unauthorized")
		return
	}
	job, ok := loadImportJob(r.Context(), tenantID, chi.URLParam(r, "job_id"))
	if !ok {
		respondError(w, http.StatusNotFound, "job not found", "not_found")
		return
	}
	errs := job.Errors

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
