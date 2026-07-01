// Package calendar provides due-date calculation logic respecting library holidays
// and branch opening hours, mirroring Koha's four due-date modes.
package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/branch"
	"github.com/bengobox/library-service/internal/ent/libraryholiday"
)

// Mode mirrors CirculationRule.due_date_mode.
type Mode string

const (
	ModeDAYS     Mode = "DAYS"     // raw calendar-day count, no adjustments
	ModeCALENDAR Mode = "CALENDAR" // skip closed days when counting
	ModeDATEDUE  Mode = "DATEDUE"  // push past any closure at the computed due date
	ModeDAYWEEK  Mode = "DAYWEEK"  // push forward to the same weekday on the next open week
)

// Calculator resolves due dates using branch opening hours and holiday closures.
type Calculator struct {
	db    *ent.Client
	cache *redis.Client
	log   *zap.Logger
}

// NewCalculator builds the calendar calculator.
func NewCalculator(db *ent.Client, cache *redis.Client, log *zap.Logger) *Calculator {
	return &Calculator{db: db, cache: cache, log: log}
}

// CalculateDueDate returns the due date for a checkout starting at `from` with
// the given loan period days and due-date mode. branchID may be uuid.Nil for
// tenant-wide holidays only.
func (c *Calculator) CalculateDueDate(ctx context.Context, tenantID, branchID uuid.UUID, from time.Time, loanDays int, mode Mode) time.Time {
	if loanDays <= 0 || mode == ModeDAYS {
		return from.AddDate(0, 0, loanDays)
	}

	closedDays := c.loadClosedDays(ctx, tenantID, branchID, from, loanDays+60)
	weekdayClosed := c.loadClosedWeekdays(ctx, tenantID, branchID)

	isClosed := func(d time.Time) bool {
		key := d.Format("2006-01-02")
		if closedDays[key] {
			return true
		}
		return weekdayClosed[d.Weekday()]
	}

	switch mode {
	case ModeCALENDAR:
		// Count only open days — advance one calendar day at a time.
		due := from
		counted := 0
		for counted < loanDays {
			due = due.AddDate(0, 0, 1)
			if !isClosed(due) {
				counted++
			}
		}
		return due

	case ModeDATEDUE:
		// Raw count first, then push forward past any closure.
		due := from.AddDate(0, 0, loanDays)
		for isClosed(due) {
			due = due.AddDate(0, 0, 1)
		}
		return due

	case ModeDAYWEEK:
		// Raw count, then push forward to the next occurrence of the same weekday
		// that lands on an open day.
		due := from.AddDate(0, 0, loanDays)
		target := due.Weekday()
		for isClosed(due) || due.Weekday() != target {
			due = due.AddDate(0, 0, 1)
			// once we've passed one week of adjustment, stop on any open day
			if due.Sub(from.AddDate(0, 0, loanDays)) > 14*24*time.Hour {
				for isClosed(due) {
					due = due.AddDate(0, 0, 1)
				}
				break
			}
		}
		return due
	}

	return from.AddDate(0, 0, loanDays)
}

// loadClosedDays returns a set of dates (YYYY-MM-DD) in [from, from+daysAhead]
// that are holidays for the branch or tenant-wide.
func (c *Calculator) loadClosedDays(ctx context.Context, tenantID, branchID uuid.UUID, from time.Time, daysAhead int) map[string]bool {
	cacheKey := fmt.Sprintf("holidays:%s:%s:%s:%d", tenantID, branchID, from.Format("2006-01"), daysAhead)
	if c.cache != nil {
		if raw, err := c.cache.Get(ctx, cacheKey).Result(); err == nil {
			var m map[string]bool
			if json.Unmarshal([]byte(raw), &m) == nil {
				return m
			}
		}
	}

	end := from.AddDate(0, 0, daysAhead)
	q := c.db.LibraryHoliday.Query().
		Where(
			libraryholiday.TenantIDEQ(tenantID),
			libraryholiday.HolidayDateGTE(from),
			libraryholiday.HolidayDateLTE(end),
		)
	if branchID != uuid.Nil {
		q = q.Where(libraryholiday.Or(
			libraryholiday.BranchIDIsNil(),
			libraryholiday.BranchIDEQ(branchID),
		))
	} else {
		q = q.Where(libraryholiday.BranchIDIsNil())
	}
	rows, err := q.All(ctx)
	if err != nil {
		c.log.Warn("holiday query failed", zap.Error(err))
		return map[string]bool{}
	}

	year := from.Year()
	result := make(map[string]bool, len(rows))
	for _, h := range rows {
		result[h.HolidayDate.Format("2006-01-02")] = true
		if h.IsRecurring {
			// Project recurring holiday into current year.
			recurring := time.Date(year, h.HolidayDate.Month(), h.HolidayDate.Day(), 0, 0, 0, 0, time.UTC)
			result[recurring.Format("2006-01-02")] = true
		}
	}

	if c.cache != nil {
		if b, err := json.Marshal(result); err == nil {
			_ = c.cache.Set(ctx, cacheKey, string(b), time.Hour).Err()
		}
	}
	return result
}

// loadClosedWeekdays returns the set of weekdays on which the branch is closed,
// derived from Branch.opening_hours (a JSON map of weekday → {"open":…,"close":…} | null).
func (c *Calculator) loadClosedWeekdays(ctx context.Context, tenantID, branchID uuid.UUID) map[time.Weekday]bool {
	if branchID == uuid.Nil {
		return map[time.Weekday]bool{}
	}
	b, err := c.db.Branch.Query().Where(branch.IDEQ(branchID), branch.TenantID(tenantID)).Only(ctx)
	if err != nil || b.OpeningHours == nil {
		return map[time.Weekday]bool{}
	}
	// opening_hours is map[string]any — keys are weekday names, null/absent means closed.
	names := map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
		"wednesday": time.Wednesday, "thursday": time.Thursday, "friday": time.Friday,
		"saturday": time.Saturday,
	}
	closed := map[time.Weekday]bool{}
	for name, wd := range names {
		v, ok := b.OpeningHours[name]
		if !ok || v == nil {
			closed[wd] = true
			continue
		}
		// If the value is a string like "" or "closed", also mark as closed.
		if s, isStr := v.(string); isStr && (s == "" || strings.EqualFold(s, "closed")) {
			closed[wd] = true
		}
	}
	return closed
}

// InvalidateHolidayCache clears cached holiday sets for a tenant so the next
// CalculateDueDate call re-fetches from DB.
func (c *Calculator) InvalidateHolidayCache(ctx context.Context, tenantID uuid.UUID) {
	if c.cache == nil {
		return
	}
	pattern := fmt.Sprintf("holidays:%s:*", tenantID.String())
	keys, err := c.cache.Keys(ctx, pattern).Result()
	if err != nil || len(keys) == 0 {
		return
	}
	_ = c.cache.Del(ctx, keys...).Err()
}
