package dataflows

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Bar struct {
	Date                   string `json:"date"`
	Open, High, Low, Close *float64
	Volume                 *float64
	Dividends, StockSplits float64
}

func floatCell(v *float64, places int) string {
	if v == nil || math.IsNaN(*v) {
		return ""
	}
	n := *v
	if places >= 0 {
		scale := math.Pow10(places)
		n = math.RoundToEven(n*scale) / scale
	}
	s := strconv.FormatFloat(n, 'f', -1, 64)
	if places != 0 && !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
func (s *Service) yahooGet(ctx context.Context, path string, q url.Values) ([]byte, error) {
	// Match stockstats_utils.yf_retry: initial request plus three retries at
	// 2, 4 and 8 seconds. Authentication refresh remains inside each attempt.
	wait := s.wait
	if wait == nil {
		wait = waitContext
	}
	for attempt := 0; attempt < 4; attempt++ {
		b, err := s.yahooGetAttempt(ctx, path, q)
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Status != 429 {
			return b, err
		}
		if attempt == 3 {
			return nil, &UnavailableError{"Yahoo Finance rate limit exceeded"}
		}
		if err = wait(ctx, time.Duration(2<<attempt)*time.Second); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("Yahoo retry budget exhausted")
}
func (s *Service) yahooGetAttempt(ctx context.Context, path string, q url.Values) ([]byte, error) {
	b, e := s.request(ctx, "GET", s.Endpoints["yahoo"]+path, q, nil, nil, 30*time.Second)
	var he *HTTPError
	if errors.As(e, &he) && he.Status == 401 {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.crumb == "" {
			_, cookieErr := s.request(ctx, "GET", "https://fc.yahoo.com", nil, nil, nil, 30*time.Second)
			if cookieErr != nil {
				var h *HTTPError
				if !errors.As(cookieErr, &h) {
					return nil, cookieErr
				}
			}
			crumb, err := s.request(ctx, "GET", s.Endpoints["yahoo"]+"/v1/test/getcrumb", nil, nil, nil, 30*time.Second)
			if err != nil {
				return nil, err
			}
			s.crumb = string(crumb)
		}
		if q == nil {
			q = url.Values{}
		}
		q.Set("crumb", s.crumb)
		return s.request(ctx, "GET", s.Endpoints["yahoo"]+path, q, nil, nil, 30*time.Second)
	}
	return b, e
}
func (s *Service) Bars(ctx context.Context, symbol, start, end string) ([]Bar, error) {
	canonical := s.Normalize(symbol)
	if _, e := SafeComponent(canonical); e != nil {
		return nil, e
	}
	a, e := ParseDate(start)
	if e != nil {
		return nil, e
	}
	b, e := ParseDate(end)
	if e != nil {
		return nil, e
	}
	q := url.Values{"period1": {strconv.FormatInt(a.Unix(), 10)}, "period2": {strconv.FormatInt(b.AddDate(0, 0, 1).Unix(), 10)}, "interval": {"1d"}, "events": {"div,splits,capitalGains"}, "includeAdjustedClose": {"true"}}
	raw, e := s.yahooGet(ctx, "/v8/finance/chart/"+url.PathEscape(canonical), q)
	if e != nil {
		return nil, e
	}
	var response struct {
		Chart struct {
			Result []struct {
				Meta struct {
					Timezone string `json:"exchangeTimezoneName"`
					Offset   int    `json:"gmtoffset"`
				}
				Timestamp  []int64
				Indicators struct {
					Quote    []struct{ Open, High, Low, Close, Volume []*float64 }
					AdjClose []struct {
						Values []*float64 `json:"adjclose"`
					} `json:"adjclose"`
				}
				Events struct {
					Dividends map[string]struct {
						Date   int64
						Amount float64
					}
					Splits map[string]struct {
						Date                   int64
						Numerator, Denominator float64
					}
				}
			}
			Error *struct{ Description string }
		}
	}
	if e = json.Unmarshal(raw, &response); e != nil {
		return nil, e
	}
	if response.Chart.Error != nil {
		return nil, &NoDataError{symbol, canonical, response.Chart.Error.Description}
	}
	if len(response.Chart.Result) == 0 {
		return nil, &NoDataError{symbol, canonical, "no price rows"}
	}
	r := response.Chart.Result[0]
	if len(r.Indicators.Quote) == 0 {
		return nil, &NoDataError{symbol, canonical, "no price rows"}
	}
	quote := r.Indicators.Quote[0]
	loc, e := time.LoadLocation(r.Meta.Timezone)
	if e != nil {
		loc = time.FixedZone(r.Meta.Timezone, r.Meta.Offset)
	}
	cell := func(v []*float64, i int) *float64 {
		if i < len(v) {
			return v[i]
		}
		return nil
	}
	bars := []Bar{}
	for i, stamp := range r.Timestamp {
		date := time.Unix(stamp, 0).In(loc).Format(dateLayout)
		if date < start || date > end {
			continue
		}
		bar := Bar{Date: date, Open: cell(quote.Open, i), High: cell(quote.High, i), Low: cell(quote.Low, i), Close: cell(quote.Close, i), Volume: cell(quote.Volume, i)}
		if bar.Close != nil && len(r.Indicators.AdjClose) > 0 {
			adj := cell(r.Indicators.AdjClose[0].Values, i)
			if adj != nil && *bar.Close != 0 {
				ratio := *adj / *bar.Close
				for _, p := range []**float64{&bar.Open, &bar.High, &bar.Low, &bar.Close} {
					if *p != nil {
						v := **p * ratio
						*p = &v
					}
				}
			}
		}
		for _, d := range r.Events.Dividends {
			if time.Unix(d.Date, 0).In(loc).Format(dateLayout) == date {
				bar.Dividends = d.Amount
			}
		}
		for _, sp := range r.Events.Splits {
			if sp.Denominator != 0 && time.Unix(sp.Date, 0).In(loc).Format(dateLayout) == date {
				bar.StockSplits = sp.Numerator / sp.Denominator
			}
		}
		bars = append(bars, bar)
	}
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	if len(bars) == 0 {
		return nil, &NoDataError{symbol, canonical, "no price rows"}
	}
	return bars, nil
}
func stale(bars []Bar, end, symbol, canonical string) error {
	if len(bars) == 0 {
		return &NoDataError{symbol, canonical, "no bar in range has a closing price"}
	}
	latest := bars[0].Date
	for _, b := range bars {
		if b.Date > latest {
			latest = b.Date
		}
	}
	last, e := ParseDate(latest)
	if e != nil {
		return e
	}
	date, e := ParseDate(end)
	if e != nil {
		return e
	}
	days := int(date.Sub(last).Hours() / 24)
	if days > 10 {
		return &NoDataError{symbol, canonical, fmt.Sprintf("latest row is %s, %d days before the requested %s (stale) — refusing to use it", last.Format(dateLayout), days, end)}
	}
	return nil
}
func AtomicWrite(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".tradingagents-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func barsCSV(bars []Bar, actions bool, places int) (string, error) {
	var out strings.Builder
	w := csv.NewWriter(&out)
	header := []string{"Date", "Open", "High", "Low", "Close", "Volume"}
	if actions {
		header = append(header, "Dividends", "Stock Splits")
	}
	if e := w.Write(header); e != nil {
		return "", e
	}
	for _, b := range bars {
		row := []string{b.Date, floatCell(b.Open, places), floatCell(b.High, places), floatCell(b.Low, places), floatCell(b.Close, places), floatCell(b.Volume, 0)}
		if actions {
			row = append(row, floatCell(&b.Dividends, -1), floatCell(&b.StockSplits, -1))
		}
		if e := w.Write(row); e != nil {
			return "", e
		}
	}
	w.Flush()
	return out.String(), w.Error()
}
func parseBarsCSV(raw []byte) ([]Bar, error) {
	reader := csv.NewReader(strings.NewReader(string(raw)))
	reader.FieldsPerRecord = -1 // pandas skips surplus fields and accepts missing cells.
	rows, e := reader.ReadAll()
	if e != nil {
		return nil, e
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("empty OHLCV cache")
	}
	cols := map[string]int{}
	for i, k := range rows[0] {
		cols[k] = i
	}
	if _, ok := cols["Date"]; !ok {
		for _, alias := range []string{"index", "Datetime", "date"} {
			if idx, ok := cols[alias]; ok {
				cols["Date"] = idx
				break
			}
		}
	}
	if _, ok := cols["Date"]; !ok {
		return nil, fmt.Errorf("OHLCV cache has no date column")
	}
	if _, ok := cols["Close"]; !ok {
		return nil, fmt.Errorf("OHLCV cache has no Close column")
	}
	cell := func(row []string, name string) *float64 {
		idx, ok := cols[name]
		if !ok || idx >= len(row) {
			return nil
		}
		f, e := strconv.ParseFloat(row[idx], 64)
		if e != nil || math.IsNaN(f) {
			return nil
		}
		return &f
	}
	bars := []Bar{}
	for _, r := range rows[1:] {
		if len(r) > len(rows[0]) || len(r) <= cols["Date"] {
			continue
		}
		date := r[cols["Date"]]
		if len(date) > 10 {
			date = date[:10]
		}
		if _, e := ParseDate(date); e != nil {
			continue
		}
		bars = append(bars, Bar{Date: date, Open: cell(r, "Open"), High: cell(r, "High"), Low: cell(r, "Low"), Close: cell(r, "Close"), Volume: cell(r, "Volume")})
	}
	return bars, nil
}
func (s *Service) LoadOHLCV(ctx context.Context, symbol, date string, fill bool) ([]Bar, error) {
	canonical := s.Normalize(symbol)
	safe, e := SafeComponent(canonical)
	if e != nil {
		return nil, e
	}
	if _, e = ParseDate(date); e != nil {
		return nil, e
	}
	path := filepath.Join(s.Config.DataCacheDir, safe+"-YFin-data.csv")
	now := s.Now()
	var bars []Bar
	if info, err := os.Stat(path); err == nil && info.ModTime().Format(dateLayout) == now.Format(dateLayout) && (date < now.Format(dateLayout) || now.Sub(info.ModTime()) <= 900*time.Second) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		bars, err = parseBarsCSV(raw)
		if err != nil {
			bars = nil
		}
	}
	if len(bars) == 0 {
		bars, e = s.Bars(ctx, symbol, now.AddDate(-5, 0, 0).Format(dateLayout), now.Format(dateLayout))
		if e != nil {
			return nil, e
		}
		raw, e := barsCSV(bars, false, -1)
		if e != nil {
			return nil, e
		}
		if e = AtomicWrite(path, []byte(raw)); e != nil {
			return nil, e
		}
	}
	out := []Bar{}
	for _, b := range bars {
		if b.Date > date || b.Close == nil || math.IsNaN(*b.Close) {
			continue
		}
		out = append(out, b)
	}
	if fill {
		// Apply ffill().bfill() only after date clipping and dropping closeless
		// rows, so an unsettled or future bar cannot supply a price or volume.
		columns := []func(*Bar) **float64{
			func(b *Bar) **float64 { return &b.Open }, func(b *Bar) **float64 { return &b.High },
			func(b *Bar) **float64 { return &b.Low }, func(b *Bar) **float64 { return &b.Volume },
		}
		for _, column := range columns {
			var previous *float64
			for i := range out {
				value := column(&out[i])
				if *value == nil {
					*value = previous
				} else {
					previous = *value
				}
			}
			var next *float64
			for i := len(out) - 1; i >= 0; i-- {
				value := column(&out[i])
				if *value == nil {
					*value = next
				} else {
					next = *value
				}
			}
		}
	}
	if e = stale(out, date, symbol, canonical); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *Service) Info(ctx context.Context, ticker string) (map[string]any, error) {
	q := url.Values{"modules": {"financialData,quoteType,defaultKeyStatistics,assetProfile,summaryDetail"}, "formatted": {"false"}, "symbol": {s.Normalize(ticker)}}
	b, e := s.yahooGet(ctx, "/v10/finance/quoteSummary/"+url.PathEscape(s.Normalize(ticker)), q)
	if e != nil {
		return nil, e
	}
	p, e := jsonObject(b)
	if e != nil {
		return nil, e
	}
	results := arr(obj(p["quoteSummary"])["result"])
	if len(results) == 0 {
		return nil, &NoDataError{ticker, s.Normalize(ticker), "no fundamental fields returned"}
	}
	out := map[string]any{}
	for _, v := range obj(results[0]) {
		for k, value := range obj(v) {
			if raw, ok := obj(value)["raw"]; ok {
				value = raw
			}
			out[k] = value
		}
	}
	return out, nil
}
func (s *Service) yahoo(ctx context.Context, method string, r Request) (string, error) {
	switch method {
	case "get_stock_data":
		bars, e := s.Bars(ctx, r.Symbol, r.StartDate, r.EndDate)
		if e != nil {
			return "", e
		}
		if e = stale(bars, r.EndDate, r.Symbol, s.Normalize(r.Symbol)); e != nil {
			return "", e
		}
		csv, e := barsCSV(bars, true, 2)
		if e != nil {
			return "", e
		}
		label := s.Normalize(r.Symbol)
		if label != strings.ToUpper(r.Symbol) {
			label += " (from " + r.Symbol + ")"
		}
		return fmt.Sprintf("# Stock data for %s from %s to %s\n# Total records: %d\n# Data retrieved on: %s\n\n%s", label, r.StartDate, r.EndDate, len(bars), s.Now().Format("2006-01-02 15:04:05"), csv), nil
	case "get_indicators":
		return s.indicator(ctx, r)
	case "get_news":
		return s.yahooNews(ctx, r)
	case "get_global_news":
		return s.yahooGlobalNews(ctx, r)
	case "get_balance_sheet", "get_income_statement", "get_cashflow":
		return s.yahooStatement(ctx, method, r)
	case "get_insider_transactions":
		return s.yahooInsiders(ctx, r)
	case "get_fundamentals":
		label := s.Normalize(r.Ticker)
		if withheld := WithholdProfile(r.CurrentDate, label, s.Now().Format(dateLayout)); withheld != "" {
			return withheld, nil
		}
		info, e := s.Info(ctx, r.Ticker)
		if e != nil {
			return "", e
		}
		fields := [][2]string{{"Name", "longName"}, {"Sector", "sector"}, {"Industry", "industry"}, {"Market Cap", "marketCap"}, {"PE Ratio (TTM)", "trailingPE"}, {"Forward PE", "forwardPE"}, {"PEG Ratio", "pegRatio"}, {"Price to Book", "priceToBook"}, {"EPS (TTM)", "trailingEps"}, {"Forward EPS", "forwardEps"}, {"Dividend Yield", "dividendYield"}, {"Beta", "beta"}, {"52 Week High", "fiftyTwoWeekHigh"}, {"52 Week Low", "fiftyTwoWeekLow"}, {"50 Day Average", "fiftyDayAverage"}, {"200 Day Average", "twoHundredDayAverage"}, {"Revenue (TTM)", "totalRevenue"}, {"Gross Profit", "grossProfits"}, {"EBITDA", "ebitda"}, {"Net Income", "netIncomeToCommon"}, {"Profit Margin", "profitMargins"}, {"Operating Margin", "operatingMargins"}, {"Return on Equity", "returnOnEquity"}, {"Return on Assets", "returnOnAssets"}, {"Debt to Equity", "debtToEquity"}, {"Current Ratio", "currentRatio"}, {"Book Value", "bookValue"}, {"Free Cash Flow", "freeCashflow"}}
		lines := []string{}
		for _, f := range fields {
			if v := info[f[1]]; v != nil {
				lines = append(lines, f[0]+": "+text(v))
			}
		}
		if len(lines) == 0 {
			return "", &NoDataError{r.Ticker, label, "no fundamental fields returned"}
		}
		return fmt.Sprintf("# Company Fundamentals for %s\n# Data retrieved on: %s\n\n%s", label, s.Now().Format("2006-01-02 15:04:05"), strings.Join(lines, "\n")), nil
	}
	return "", fmt.Errorf("Yahoo method unavailable: %s", method)
}
