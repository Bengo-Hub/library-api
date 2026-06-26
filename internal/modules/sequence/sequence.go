// Package sequence allocates human-readable, per-tenant monotonic numbers (membership_no,
// accession_no, loan_no) backed by the DocumentSequence table. Allocation uses
// SELECT ... FOR UPDATE inside the caller's transaction so concurrent checkouts and
// registrations never collide.
package sequence

import (
	"context"
	"fmt"

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

// Next allocates and returns the next formatted number for (tenant, kind) within tx,
// creating the counter row on first use with the given default prefix + pad width.
func Next(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, kind, defaultPrefix string, padWidth int) (string, error) {
	seq, err := tx.DocumentSequence.Query().
		Where(documentsequence.TenantID(tenantID), documentsequence.Kind(kind)).
		ForUpdate().
		Only(ctx)
	if ent.IsNotFound(err) {
		if padWidth <= 0 {
			padWidth = 5
		}
		seq, err = tx.DocumentSequence.Create().
			SetTenantID(tenantID).
			SetKind(kind).
			SetPrefix(defaultPrefix).
			SetNextValue(1).
			SetPadWidth(padWidth).
			Save(ctx)
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}

	val := seq.NextValue
	if _, err := tx.DocumentSequence.UpdateOne(seq).SetNextValue(val + 1).Save(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%0*d", seq.Prefix, seq.PadWidth, val), nil
}
