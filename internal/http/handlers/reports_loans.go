package handlers

import (
	"context"
	"sort"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/finepayment"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
)

// The loan analytics below group in SQL and look up names only for the rows that are shown.
// They replace per-loan row scans with a per-copy or per-member query, so cost follows the
// size of the catalogue or membership, not the number of loans ever made.

type trendPoint struct {
	Date      string `json:"date"`
	Checkouts int    `json:"checkouts"`
	Returns   int    `json:"returns"`
}

// circulationTrend returns daily checkout and return counts for the last n days (oldest
// first). Returns are counted on the day they happen, whenever the loan began.
func (h *ReportsHandler) circulationTrend(ctx context.Context, tenantID uuid.UUID, days int) []trendPoint {
	since := time.Now().AddDate(0, 0, -days)
	out := make([]trendPoint, 0, days)
	idx := map[string]int{}
	for i := days - 1; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		idx[d] = len(out)
		out = append(out, trendPoint{Date: d})
	}
	for _, t := range h.loanTimes(ctx, h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.CheckoutAtGTE(since)), loan.FieldCheckoutAt) {
		if i, ok := idx[t.Format("2006-01-02")]; ok {
			out[i].Checkouts++
		}
	}
	for _, t := range h.loanTimes(ctx, h.db.Loan.Query().Where(loan.TenantID(tenantID), loan.ReturnedAtGTE(since)), loan.FieldReturnedAt) {
		if i, ok := idx[t.Format("2006-01-02")]; ok {
			out[i].Returns++
		}
	}
	return out
}

type popularTitle struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Loans int    `json:"loans"`
}

// loansPerCopy counts loans checked out since the cutoff, grouped by copy.
func (h *ReportsHandler) loansPerCopy(ctx context.Context, tenantID uuid.UUID, since time.Time) map[uuid.UUID]int {
	var rows []struct {
		CopyID uuid.UUID `json:"copy_id"`
		Count  int       `json:"count"`
	}
	out := map[uuid.UUID]int{}
	if err := h.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.CheckoutAtGTE(since)).
		GroupBy(loan.FieldCopyID).Aggregate(ent.Count()).Scan(ctx, &rows); err != nil {
		return out
	}
	for _, r := range rows {
		out[r.CopyID] = r.Count
	}
	return out
}

// popularTitles returns the top-n most borrowed titles since the cutoff.
func (h *ReportsHandler) popularTitles(ctx context.Context, tenantID uuid.UUID, since time.Time, limit int) []popularTitle {
	perCopy := h.loansPerCopy(ctx, tenantID, since)
	copyIDs := make([]uuid.UUID, 0, len(perCopy))
	for id := range perCopy {
		copyIDs = append(copyIDs, id)
	}
	perBib := map[uuid.UUID]int{}
	for _, chunk := range chunkIDs(copyIDs, 1000) {
		var rows []struct {
			ID          uuid.UUID `json:"id"`
			BibRecordID uuid.UUID `json:"bib_record_id"`
		}
		if err := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID), bookcopy.IDIn(chunk...)).
			Select(bookcopy.FieldID, bookcopy.FieldBibRecordID).Scan(ctx, &rows); err != nil {
			continue
		}
		for _, c := range rows {
			perBib[c.BibRecordID] += perCopy[c.ID]
		}
	}
	ranked := make([]popularTitle, 0, len(perBib))
	for id, n := range perBib {
		ranked = append(ranked, popularTitle{ID: id.String(), Loans: n})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Loans != ranked[j].Loans {
			return ranked[i].Loans > ranked[j].Loans
		}
		return ranked[i].ID < ranked[j].ID
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	ids := make([]uuid.UUID, 0, len(ranked))
	for _, t := range ranked {
		if id, err := uuid.Parse(t.ID); err == nil {
			ids = append(ids, id)
		}
	}
	titles := h.bibTitles(ctx, tenantID, ids)
	for i := range ranked {
		if id, err := uuid.Parse(ranked[i].ID); err == nil {
			ranked[i].Title = titles[id]
		}
	}
	return ranked
}

// bibTitles resolves titles for a small set of bib ids in one query.
func (h *ReportsHandler) bibTitles(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out
	}
	bibs, _ := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID), bibrecord.IDIn(ids...)).
		Select(bibrecord.FieldID, bibrecord.FieldTitle).All(ctx)
	for _, b := range bibs {
		out[b.ID] = b.Title
	}
	return out
}

type memberRef struct{ Name, No string }

// memberRefs resolves display name and membership number for a set of members in one query.
func (h *ReportsHandler) memberRefs(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) map[uuid.UUID]memberRef {
	out := map[uuid.UUID]memberRef{}
	for _, chunk := range chunkIDs(ids, 1000) {
		ms, _ := h.db.Member.Query().Where(member.TenantID(tenantID), member.IDIn(chunk...)).
			Select(member.FieldID, member.FieldDisplayName, member.FieldMembershipNo).All(ctx)
		for _, m := range ms {
			out[m.ID] = memberRef{Name: m.DisplayName, No: m.MembershipNo}
		}
	}
	return out
}

// chunkIDs splits ids into slices of at most n, keeping IN lists inside Postgres' parameter limit.
func chunkIDs(ids []uuid.UUID, n int) [][]uuid.UUID {
	var out [][]uuid.UUID
	for len(ids) > n {
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

// collectionValue is the total acquisition cost of the tenant's copies, summed in SQL.
func (h *ReportsHandler) collectionValue(ctx context.Context, tenantID uuid.UUID) decimal.Decimal {
	var rows []struct {
		Value string `json:"value"`
	}
	err := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID)).
		Aggregate(func(s *sql.Selector) string {
			return sql.As("COALESCE(SUM("+s.C(bookcopy.FieldAcquisitionCost)+"), 0)", "value")
		}).Scan(ctx, &rows)
	if err != nil || len(rows) == 0 {
		return decimal.Zero
	}
	d, _ := decimal.NewFromString(rows[0].Value)
	return d
}

// collectedSince is the money received at the desk or through treasury since the cutoff,
// summed from the payment ledger (so part payments count on the day they were taken).
func (h *ReportsHandler) collectedSince(ctx context.Context, tenantID uuid.UUID, since time.Time) decimal.Decimal {
	var rows []struct {
		Collected string `json:"collected"`
	}
	err := h.db.FinePayment.Query().
		Where(finepayment.TenantID(tenantID), finepayment.PaidAtGTE(since)).
		Aggregate(func(s *sql.Selector) string {
			return sql.As("COALESCE(SUM("+s.C(finepayment.FieldAmount)+"), 0)", "collected")
		}).Scan(ctx, &rows)
	if err != nil || len(rows) == 0 {
		return decimal.Zero
	}
	d, _ := decimal.NewFromString(rows[0].Collected)
	return d
}

// loanTimes reads a single timestamp column for the loans a query matches, without loading
// the full rows.
func (h *ReportsHandler) loanTimes(ctx context.Context, q *ent.LoanQuery, field string) []time.Time {
	// ent scans a partial row into the entity struct by json tag, so only one column is read.
	var rows []ent.Loan
	if err := q.Select(field).Scan(ctx, &rows); err != nil {
		h.log.Warn("loan timestamp query failed", zap.String("column", field), zap.Error(err))
		return nil
	}
	out := make([]time.Time, 0, len(rows))
	for _, l := range rows {
		switch field {
		case loan.FieldCheckoutAt:
			out = append(out, l.CheckoutAt)
		case loan.FieldDueAt:
			out = append(out, l.DueAt)
		case loan.FieldReturnedAt:
			if l.ReturnedAt != nil {
				out = append(out, *l.ReturnedAt)
			}
		}
	}
	return out
}
