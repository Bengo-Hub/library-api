// Package sequence allocates human-readable, per-tenant monotonic numbers (membership_no,
// accession_no, loan_no) backed by the DocumentSequence table. Allocation uses
// SELECT ... FOR UPDATE inside the caller's transaction so concurrent checkouts and
// registrations never collide. Numbers are rendered from a configurable template and the
// counter can reset yearly/monthly (adapted from the treasury-api document-sequence pattern).
package sequence

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/documentsequence"
)

// Kinds of sequences.
const (
	KindMembership = "membership_no"
	KindAccession  = "accession_no"
	KindLoan       = "loan_no"
)

// Reset periods.
const (
	ResetNone    = "none"
	ResetYearly  = "yearly"
	ResetMonthly = "monthly"
)

// DefaultFormat returns the template a sequence kind is created with when none is configured.
func DefaultFormat(kind string) string {
	switch kind {
	case KindMembership:
		return "{prefix}/{seq}/{yy}"
	default:
		return "{prefix}{seq}"
	}
}

// defaultReset returns the reset period a sequence kind is created with.
func defaultReset(kind string) string {
	if kind == KindMembership {
		return ResetYearly
	}
	return ResetNone
}

// periodKey is the marker compared to detect a reset boundary for the given reset period.
func periodKey(reset string, now time.Time) string {
	switch reset {
	case ResetYearly:
		return now.Format("2006")
	case ResetMonthly:
		return now.Format("2006-01")
	default:
		return ""
	}
}

// Render fills a template ({prefix} {seq} {yy} {yyyy} {mm}) with a value at a point in time.
func Render(format, prefix string, padWidth int, val int64, now time.Time) string {
	if format == "" {
		format = "{prefix}{seq}"
	}
	if padWidth <= 0 {
		padWidth = 5
	}
	seqStr := fmt.Sprintf("%0*d", padWidth, val)
	return strings.NewReplacer(
		"{prefix}", prefix,
		"{seq}", seqStr,
		"{yy}", now.Format("06"),
		"{yyyy}", now.Format("2006"),
		"{mm}", now.Format("01"),
	).Replace(format)
}

// Next allocates and returns the next formatted number for (tenant, kind) within tx,
// creating the counter row on first use with kind-appropriate defaults. Applies the
// configured reset period (counter restarts at 1 on a new year/month) and renders the
// number from the configured format template.
func Next(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, kind, defaultPrefix string, padWidth int) (string, error) {
	now := time.Now()
	if padWidth <= 0 {
		padWidth = 5
	}
	seq, err := tx.DocumentSequence.Query().
		Where(documentsequence.TenantID(tenantID), documentsequence.Kind(kind)).
		ForUpdate().
		Only(ctx)
	if ent.IsNotFound(err) {
		reset := defaultReset(kind)
		seq, err = tx.DocumentSequence.Create().
			SetTenantID(tenantID).
			SetKind(kind).
			SetPrefix(defaultPrefix).
			SetNextValue(1).
			SetPadWidth(padWidth).
			SetFormat(DefaultFormat(kind)).
			SetResetPeriod(reset).
			SetPeriodKey(periodKey(reset, now)).
			Save(ctx)
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}

	// Honor the reset boundary: a new period restarts the counter at 1.
	val := seq.NextValue
	upd := tx.DocumentSequence.UpdateOne(seq)
	if seq.ResetPeriod != "" && seq.ResetPeriod != ResetNone {
		curKey := periodKey(seq.ResetPeriod, now)
		if curKey != seq.PeriodKey {
			val = 1
			upd = upd.SetPeriodKey(curKey)
		}
	}
	if _, err := upd.SetNextValue(val + 1).Save(ctx); err != nil {
		return "", err
	}
	return Render(seq.Format, seq.Prefix, seq.PadWidth, val, now), nil
}
