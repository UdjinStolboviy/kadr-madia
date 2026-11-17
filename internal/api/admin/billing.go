package admin

import (
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// BillingRates are the unit prices used for invoice previews. Amounts are in
// the smallest sensible unit of Currency (e.g. dollars as decimals).
type BillingRates struct {
	Currency          string  `json:"currency"`
	PerGBStoredMonth  float64 `json:"per_gb_stored_month"` // average stored GB over the month
	PerGBIngested     float64 `json:"per_gb_ingested"`     // original bytes uploaded
	PerVideoMinute    float64 `json:"per_video_minute"`    // processed video minutes
	Per1000Uploads    float64 `json:"per_1000_uploads"`
	MinimumMonthlyFee float64 `json:"minimum_monthly_fee"`
}

const billingRatesKey = "billing.rates"

func (s *Server) loadRates(r *http.Request) (BillingRates, error) {
	rates := BillingRates{Currency: "USD"}
	if err := s.store.GetSetting(r.Context(), billingRatesKey, &rates); err != nil && !errors.Is(err, db.ErrNotFound) {
		return rates, err
	}
	return rates, nil
}

func (s *Server) handleGetBillingRates(w http.ResponseWriter, r *http.Request) {
	rates, err := s.loadRates(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rates)
}

func (s *Server) handlePutBillingRates(w http.ResponseWriter, r *http.Request) {
	var in BillingRates
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		badJSON(w, err)
		return
	}
	errs := map[string]string{}
	if len(in.Currency) != 3 {
		errs["currency"] = "ISO 4217 code, e.g. USD"
	}
	for k, v := range map[string]float64{"per_gb_stored_month": in.PerGBStoredMonth, "per_gb_ingested": in.PerGBIngested,
		"per_video_minute": in.PerVideoMinute, "per_1000_uploads": in.Per1000Uploads, "minimum_monthly_fee": in.MinimumMonthlyFee} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || v > 1e6 {
			errs[k] = "must be between 0 and 1,000,000"
		}
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	err := s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.SetSetting(r.Context(), billingRatesKey, in, s.now()); err != nil {
			return err
		}
		return s.audit(r, tx, "billing.rates", nil, nil, in)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, in)
}

type billingLine struct {
	Item      string  `json:"item"`
	Quantity  float64 `json:"quantity"`
	Unit      string  `json:"unit"`
	UnitPrice float64 `json:"unit_price"`
	Amount    float64 `json:"amount"`
}

type billingReport struct {
	Project  string        `json:"project_id"`
	Month    string        `json:"month"`
	Currency string        `json:"currency"`
	Lines    []billingLine `json:"lines"`
	Subtotal float64       `json:"subtotal"`
	Total    float64       `json:"total"`
	Usage    usageDayDTO   `json:"usage"`
	Days     []usageDayDTO `json:"days"`
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// buildBilling metering for one project and month. Stored GB is the mean over
// the days of the month of the cumulative stored bytes recorded in usage_daily
// (bytes_stored is a per-day delta of new renditions, so this is an estimate
// that ignores deletions; the live figure is project.used_bytes).
func (s *Server) buildBilling(r *http.Request, p *db.Project, month time.Time) (*billingReport, error) {
	rates, err := s.loadRates(r)
	if err != nil {
		return nil, err
	}
	from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, -1)
	rows, err := s.store.ListUsageByDay(r.Context(), p.ID, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	rep := &billingReport{Project: p.ID, Month: from.Format("2006-01"), Currency: rates.Currency, Days: []usageDayDTO{}}
	for _, x := range rows {
		d := usageDayDTO{Day: x.Day, Uploads: x.Uploads, AssetsReady: x.AssetsReady, AssetsFailed: x.AssetsFailed, BytesIn: x.BytesIn,
			BytesStored: x.BytesStored, VideoSec: x.VideoSec, CPUMillis: x.CPUMillis, DedupHits: x.DedupHits}
		rep.Days = append(rep.Days, d)
		rep.Usage.Uploads += d.Uploads
		rep.Usage.AssetsReady += d.AssetsReady
		rep.Usage.BytesIn += d.BytesIn
		rep.Usage.BytesStored += d.BytesStored
		rep.Usage.VideoSec += d.VideoSec
	}
	const gb = 1 << 30
	storedGB := float64(p.UsedBytes) / gb // current footprint, billed for the month
	add := func(item, unit string, qty, price float64) {
		rep.Lines = append(rep.Lines, billingLine{Item: item, Quantity: round2(qty), Unit: unit, UnitPrice: price, Amount: round2(qty * price)})
		rep.Subtotal += qty * price
	}
	add("Storage", "GB-month", storedGB, rates.PerGBStoredMonth)
	add("Ingested originals", "GB", float64(rep.Usage.BytesIn)/gb, rates.PerGBIngested)
	add("Video processing", "minute", float64(rep.Usage.VideoSec)/60, rates.PerVideoMinute)
	add("Uploads", "1000 uploads", float64(rep.Usage.Uploads)/1000, rates.Per1000Uploads)
	rep.Subtotal = round2(rep.Subtotal)
	rep.Total = math.Max(rep.Subtotal, rates.MinimumMonthlyFee)
	return rep, nil
}

// handleBilling: GET /admin/api/projects/{id}/billing?month=YYYY-MM[&format=csv]
// is an invoice preview from metered usage and the configured rates.
func (s *Server) handleBilling(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	month := s.now().UTC()
	if m := r.URL.Query().Get("month"); m != "" {
		t, err := time.Parse("2006-01", m)
		if err != nil {
			validationFailed(w, map[string]string{"month": "must be YYYY-MM"})
			return
		}
		month = t
	}
	rep, err := s.buildBilling(r, p, month)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if r.URL.Query().Get("format") != "csv" {
		httpx.WriteJSON(w, http.StatusOK, rep)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="usage-%s-%s.csv"`, p.Slug, rep.Month))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"day", "uploads", "assets_ready", "assets_failed", "bytes_in", "bytes_stored", "video_sec", "cpu_ms", "dedup_hits"})
	for _, d := range rep.Days {
		_ = cw.Write([]string{d.Day, i64(d.Uploads), i64(d.AssetsReady), i64(d.AssetsFailed), i64(d.BytesIn), i64(d.BytesStored),
			i64(d.VideoSec), i64(d.CPUMillis), i64(d.DedupHits)})
	}
	cw.Flush()
}

func i64(v int64) string { return strconv.FormatInt(v, 10) }
