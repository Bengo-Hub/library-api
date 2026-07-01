package handlers

import (
	"encoding/csv"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/bengobox/library-service/internal/ent/acquisitionfund"
	"github.com/bengobox/library-service/internal/ent/bibrecord"
	entvendor "github.com/bengobox/library-service/internal/ent/vendor"
	"github.com/bengobox/library-service/internal/ent/bookcopy"
	"github.com/bengobox/library-service/internal/ent/ebookloan"
	"github.com/bengobox/library-service/internal/ent/fine"
	"github.com/bengobox/library-service/internal/ent/loan"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/purchaseorder"
)

// wantCSV returns true when the caller requests CSV output (?format=csv).
func wantCSV(r *http.Request) bool {
	return r.URL.Query().Get("format") == "csv"
}

func writeCSV(w http.ResponseWriter, filename string, header []string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	_ = cw.WriteAll(rows)
	cw.Flush()
}

// MemberActivity godoc
// @Summary Most/least active borrowers in a date range
// @Tags Reports
// @Router /{tenant}/library/reports/member-activity [get]
func (h *ReportsHandler) MemberActivity(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	days := daysParam(r, 90)
	since := time.Now().AddDate(0, 0, -days)
	limit := 20

	loans, _ := h.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.CheckoutAtGTE(since)).All(ctx)

	counts := map[string]int{}
	for _, l := range loans {
		counts[l.MemberID.String()]++
	}

	type row struct {
		MemberID     string `json:"member_id"`
		Name         string `json:"name"`
		MembershipNo string `json:"membership_no"`
		Loans        int    `json:"loans"`
	}
	rows := make([]row, 0, len(counts))
	for mid, n := range counts {
		r := row{MemberID: mid, Loans: n}
		id, err := ParseUUIDParam(mid)
		if err == nil {
			if m, err := h.db.Member.Get(ctx, id); err == nil {
				r.Name = m.DisplayName
				r.MembershipNo = m.MembershipNo
			}
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Loans > rows[j].Loans })
	if len(rows) > limit {
		rows = rows[:limit]
	}

	if wantCSV(r) {
		csvRows := make([][]string, len(rows))
		for i, r := range rows {
			csvRows[i] = []string{r.MembershipNo, r.Name, strconv.Itoa(r.Loans)}
		}
		writeCSV(w, "member-activity.csv", []string{"membership_no", "name", "loans"}, csvRows)
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// OverdueAging godoc
// @Summary Overdue loans bucketed by age
// @Tags Reports
// @Router /{tenant}/library/reports/overdue-aging [get]
func (h *ReportsHandler) OverdueAging(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	now := time.Now()

	loans, _ := h.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.StatusIn(loan.StatusACTIVE, loan.StatusOVERDUE), loan.DueAtLT(now)).
		All(ctx)

	buckets := map[string]int{"0-7": 0, "8-14": 0, "15-30": 0, "31+": 0}
	for _, l := range loans {
		days := int(now.Sub(l.DueAt).Hours() / 24)
		switch {
		case days <= 7:
			buckets["0-7"]++
		case days <= 14:
			buckets["8-14"]++
		case days <= 30:
			buckets["15-30"]++
		default:
			buckets["31+"]++
		}
	}

	type row struct {
		Bucket string `json:"bucket"`
		Count  int    `json:"count"`
	}
	order := []string{"0-7", "8-14", "15-30", "31+"}
	rows := make([]row, 0, 4)
	for _, b := range order {
		rows = append(rows, row{Bucket: b, Count: buckets[b]})
	}

	if wantCSV(r) {
		csvRows := make([][]string, len(rows))
		for i, r := range rows {
			csvRows[i] = []string{r.Bucket, strconv.Itoa(r.Count)}
		}
		writeCSV(w, "overdue-aging.csv", []string{"days_overdue", "count"}, csvRows)
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// ItemMovement godoc
// @Summary Copies ranked by loan frequency
// @Tags Reports
// @Router /{tenant}/library/reports/item-movement [get]
func (h *ReportsHandler) ItemMovement(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	days := daysParam(r, 90)
	since := time.Now().AddDate(0, 0, -days)

	loans, _ := h.db.Loan.Query().
		Where(loan.TenantID(tenantID), loan.CheckoutAtGTE(since)).All(ctx)

	counts := map[string]int{}
	for _, l := range loans {
		counts[l.CopyID.String()]++
	}

	type row struct {
		CopyID   string `json:"copy_id"`
		Barcode  string `json:"barcode"`
		Title    string `json:"title"`
		Checkouts int   `json:"checkouts"`
	}
	rows := make([]row, 0, len(counts))
	for cid, n := range counts {
		r := row{CopyID: cid, Checkouts: n}
		id, err := ParseUUIDParam(cid)
		if err == nil {
			if c, err := h.db.BookCopy.Get(ctx, id); err == nil {
				r.Barcode = c.Barcode
				if b, err := h.db.BibRecord.Get(ctx, c.BibRecordID); err == nil {
					r.Title = b.Title
				}
			}
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Checkouts > rows[j].Checkouts })
	limit := 50
	if len(rows) > limit {
		rows = rows[:limit]
	}

	if wantCSV(r) {
		csvRows := make([][]string, len(rows))
		for i, r := range rows {
			csvRows[i] = []string{r.Barcode, r.Title, strconv.Itoa(r.Checkouts)}
		}
		writeCSV(w, "item-movement.csv", []string{"barcode", "title", "checkouts"}, csvRows)
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// FineAging godoc
// @Summary Outstanding fines grouped by age and member
// @Tags Reports
// @Router /{tenant}/library/reports/fine-aging [get]
func (h *ReportsHandler) FineAging(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	now := time.Now()

	fines, _ := h.db.Fine.Query().
		Where(fine.TenantID(tenantID), fine.StatusIn(fine.StatusUNPAID, fine.StatusPARTIAL)).
		All(ctx)

	type fineRow struct {
		MemberID     string  `json:"member_id"`
		Name         string  `json:"name"`
		MembershipNo string  `json:"membership_no"`
		DaysOld      int     `json:"days_old"`
		Outstanding  float64 `json:"outstanding"`
	}
	rows := make([]fineRow, 0, len(fines))
	for _, f := range fines {
		age := 0
		if f.AssessedAt != nil {
			age = int(now.Sub(*f.AssessedAt).Hours() / 24)
		}
		outstanding := f.Amount.Sub(f.AmountPaid).InexactFloat64()
		r := fineRow{
			MemberID:    f.MemberID.String(),
			Outstanding: outstanding,
			DaysOld:     age,
		}
		if m, err := h.db.Member.Get(ctx, f.MemberID); err == nil {
			r.Name = m.DisplayName
			r.MembershipNo = m.MembershipNo
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].DaysOld > rows[j].DaysOld })

	if wantCSV(r) {
		csvRows := make([][]string, len(rows))
		for i, r := range rows {
			csvRows[i] = []string{r.MembershipNo, r.Name, strconv.Itoa(r.DaysOld), strconv.FormatFloat(r.Outstanding, 'f', 2, 64)}
		}
		writeCSV(w, "fine-aging.csv", []string{"membership_no", "name", "days_old", "outstanding"}, csvRows)
		return
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: rows, Total: len(rows)})
}

// AcquisitionSpend godoc
// @Summary PO spend by vendor and fund
// @Tags Reports
// @Router /{tenant}/library/reports/acquisition-spend [get]
func (h *ReportsHandler) AcquisitionSpend(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()

	funds, _ := h.db.AcquisitionFund.Query().
		Where(acquisitionfund.TenantID(tenantID)).All(ctx)

	orders, _ := h.db.PurchaseOrder.Query().
		Where(purchaseorder.TenantID(tenantID), purchaseorder.StatusIn(
			purchaseorder.StatusSUBMITTED, purchaseorder.StatusPARTIAL, purchaseorder.StatusRECEIVED,
		)).All(ctx)

	type fundRow struct {
		FundID    string  `json:"fund_id"`
		Name      string  `json:"name"`
		Allocated float64 `json:"allocated"`
		Spent     float64 `json:"spent"`
		Remaining float64 `json:"remaining"`
	}
	fundRows := make([]fundRow, 0, len(funds))
	for _, f := range funds {
		remaining := f.AllocatedAmount.Sub(f.Spent)
		fundRows = append(fundRows, fundRow{
			FundID:    f.ID.String(),
			Name:      f.Name,
			Allocated: f.AllocatedAmount.InexactFloat64(),
			Spent:     f.Spent.InexactFloat64(),
			Remaining: remaining.InexactFloat64(),
		})
	}

	// Spend by vendor.
	vendorSpend := map[string]decimal.Decimal{}
	for _, o := range orders {
		vid := o.VendorID.String()
		vendorSpend[vid] = vendorSpend[vid].Add(o.Total)
	}
	type vendorRow struct {
		VendorID string  `json:"vendor_id"`
		Name     string  `json:"name"`
		Spend    float64 `json:"spend"`
	}
	vendorRows := make([]vendorRow, 0, len(vendorSpend))
	for vid, spend := range vendorSpend {
		vr := vendorRow{VendorID: vid, Spend: spend.InexactFloat64()}
		id, err := ParseUUIDParam(vid)
		if err == nil {
			if v, err := h.db.Vendor.Query().Where(entvendor.TenantID(tenantID), entvendor.ID(id)).First(ctx); err == nil {
				vr.Name = v.Name
			}
		}
		vendorRows = append(vendorRows, vr)
	}
	sort.Slice(vendorRows, func(i, j int) bool { return vendorRows[i].Spend > vendorRows[j].Spend })

	if wantCSV(r) {
		var csvRows [][]string
		for _, f := range fundRows {
			csvRows = append(csvRows, []string{"fund", f.Name,
				strconv.FormatFloat(f.Allocated, 'f', 2, 64),
				strconv.FormatFloat(f.Spent, 'f', 2, 64),
				strconv.FormatFloat(f.Remaining, 'f', 2, 64),
			})
		}
		writeCSV(w, "acquisition-spend.csv", []string{"type", "name", "allocated", "spent", "remaining"}, csvRows)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"funds":   fundRows,
		"vendors": vendorRows,
	})
}

// CatalogStats godoc
// @Summary Title and copy counts by format, language, and collection
// @Tags Reports
// @Router /{tenant}/library/reports/catalog-stats [get]
func (h *ReportsHandler) CatalogStats(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()

	bibs, _ := h.db.BibRecord.Query().Where(bibrecord.TenantID(tenantID)).All(ctx)
	copies, _ := h.db.BookCopy.Query().Where(bookcopy.TenantID(tenantID)).All(ctx)

	byFormat := map[string]int{}
	byLang := map[string]int{}
	for _, b := range bibs {
		byFormat[b.Format.String()]++
		lang := b.Language
		if lang == "" {
			lang = "unknown"
		}
		byLang[lang]++
	}

	byStatus := map[string]int{}
	for _, c := range copies {
		byStatus[c.Status.String()]++
	}

	type kv struct {
		Key   string `json:"key"`
		Count int    `json:"count"`
	}
	toKV := func(m map[string]int) []kv {
		out := make([]kv, 0, len(m))
		for k, v := range m {
			out = append(out, kv{k, v})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
		return out
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"total_titles": len(bibs),
		"total_copies": len(copies),
		"by_format":    toKV(byFormat),
		"by_language":  toKV(byLang),
		"by_copy_status": toKV(byStatus),
	})
}

// EbookUsage godoc
// @Summary CDL e-book loan usage and license utilization
// @Tags Reports
// @Router /{tenant}/library/reports/ebook-usage [get]
func (h *ReportsHandler) EbookUsage(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	days := daysParam(r, 30)
	since := time.Now().AddDate(0, 0, -days)

	loans, _ := h.db.EbookLoan.Query().
		Where(ebookloan.TenantID(tenantID), ebookloan.IssuedAtGTE(since)).All(ctx)

	type ebookStat struct {
		EbookID   string  `json:"ebook_id"`
		Title     string  `json:"title"`
		Loans     int     `json:"loans"`
		AvgHours  float64 `json:"avg_hours"`
	}
	statsMap := map[string]*ebookStat{}
	totalHours := map[string]float64{}
	for _, l := range loans {
		eid := l.EbookID.String()
		if _, ok := statsMap[eid]; !ok {
			statsMap[eid] = &ebookStat{EbookID: eid}
		}
		statsMap[eid].Loans++
		returnTime := l.ExpiresAt
		if l.ReturnedAt != nil {
			returnTime = *l.ReturnedAt
		}
		totalHours[eid] += returnTime.Sub(l.IssuedAt).Hours()
	}

	// Resolve titles via BibRecord.
	for eid, stat := range statsMap {
		if id, err := ParseUUIDParam(eid); err == nil {
			if e, err := h.db.Ebook.Get(ctx, id); err == nil {
				if b, err := h.db.BibRecord.Get(ctx, e.BibRecordID); err == nil {
					stat.Title = b.Title
				}
			}
		}
		if stat.Loans > 0 {
			stat.AvgHours = totalHours[eid] / float64(stat.Loans)
		}
	}

	rows := make([]ebookStat, 0, len(statsMap))
	for _, s := range statsMap {
		rows = append(rows, *s)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Loans > rows[j].Loans })

	// Current active loans (utilization).
	activeLoanCount, _ := h.db.EbookLoan.Query().
		Where(ebookloan.TenantID(tenantID), ebookloan.ReturnedAtIsNil(), ebookloan.ExpiresAtGT(time.Now())).Count(ctx)

	respondJSON(w, http.StatusOK, map[string]any{
		"total_loans":        len(loans),
		"active_loans_now":   activeLoanCount,
		"popular_titles":     rows,
	})
}

// MemberActivityTrend — member registrations per day (for membership trend chart).
func (h *ReportsHandler) MemberActivityTrend(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantUUID(r)
	ctx := r.Context()
	days := daysParam(r, 30)
	since := time.Now().AddDate(0, 0, -days)

	members, _ := h.db.Member.Query().
		Where(member.TenantID(tenantID), member.CreatedAtGTE(since)).All(ctx)

	byDay := map[string]int{}
	for _, m := range members {
		byDay[m.CreatedAt.Format("2006-01-02")]++
	}
	type point struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
	}
	out := make([]point, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, point{Date: d, Count: byDay[d]})
	}
	respondJSON(w, http.StatusOK, listEnvelope{Data: out, Total: len(out)})
}
