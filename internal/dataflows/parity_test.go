package dataflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"tradingagents/internal/config"
)

func testService(t *testing.T) *Service {
	t.Helper()
	c, e := config.Default()
	if e != nil {
		t.Fatal(e)
	}
	c.DataCacheDir = t.TempDir()
	s, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestPythonIndicators(t *testing.T) {
	b, e := os.ReadFile("testdata/python_indicators.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Bars       []Bar
		Indicators map[string][]*float64
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	got := Indicators(f.Bars)
	for name, values := range f.Indicators {
		for i, want := range values {
			n := got[name][i]
			if want == nil {
				if !math.IsNaN(n) {
					t.Errorf("%s[%d] expected NaN, got %v", name, i, n)
				}
			} else if math.Abs(n-*want) > 1e-9*math.Max(1, math.Abs(*want)) {
				t.Errorf("%s[%d] got %.14g want %.14g", name, i, n, *want)
			}
		}
	}
}
func TestPythonSECVintages(t *testing.T) {
	b, e := os.ReadFile("testdata/python_sec.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Tickers, Facts json.RawMessage
		Cases          []struct{ Method, Date, Frequency, Output string }
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if !strings.Contains(r.Header.Get("User-Agent"), "@") {
			t.Error("missing SEC contact")
		}
		if strings.Contains(r.URL.Path, "company_tickers") {
			w.Write(f.Tickers)
		} else {
			w.Write(f.Facts)
		}
	}))
	defer server.Close()
	s := testService(t)
	s.Endpoints["sec"], s.Endpoints["sec_data"] = server.URL, server.URL
	for _, c := range f.Cases {
		got, e := s.sec(context.Background(), c.Method, Request{Ticker: "AAPL", CurrentDate: c.Date, Frequency: c.Frequency})
		if e != nil {
			t.Fatal(e)
		}
		if got != c.Output {
			t.Errorf("%s %s:\ngot %s\nwant %s", c.Method, c.Date, got, c.Output)
		}
	}
	if requests != 2 {
		t.Fatalf("expected cached ticker/facts requests; got %d", requests)
	}
}
func TestDatesSymbolsAndRouting(t *testing.T) {
	s := testService(t)
	for input, want := range map[string]string{"BTCUSDT": "BTC-USD", "XAUUSD+": "GC=F", "700.HK": "0700.HK", "09992.HK": "9992.HK", "600519.SH": "600519.SS", "EURUSD": "EURUSD=X"} {
		if got := s.Normalize(input); got != want {
			t.Errorf("%s -> %s, want %s", input, got, want)
		}
	}
	a, b := AsOfWindow("2025-06-20", "2025-06-25", "2025-06-10")
	if a != "2025-06-05" || b != "2025-06-10" {
		t.Fatal(a, b)
	}
	core := errors.New("primary failed")
	calls := []string{}
	s.Vendors["get_stock_data"]["yfinance"] = func(ctx context.Context, r Request) (string, error) {
		calls = append(calls, "yfinance")
		return "", core
	}
	s.Vendors["get_stock_data"]["alpha_vantage"] = func(ctx context.Context, r Request) (string, error) {
		calls = append(calls, "alpha_vantage")
		return "fallback", nil
	}
	_, e := s.Route(context.Background(), "get_stock_data", Request{})
	if !errors.Is(e, core) || len(calls) != 1 {
		t.Fatal(calls, e)
	}
	s.Config.ToolVendors["get_stock_data"] = "yfinance,alpha_vantage"
	out, e := s.Route(context.Background(), "get_stock_data", Request{})
	if e != nil || out != "fallback" {
		t.Fatal(out, e)
	}
	s.Vendors["get_stock_data"]["alpha_vantage"] = func(context.Context, Request) (string, error) { return "", &NoDataError{"X", "Y", "stale"} }
	out, e = s.Route(context.Background(), "get_stock_data", Request{})
	if e != nil || !strings.Contains(out, "NO_DATA_AVAILABLE") || !strings.Contains(out, "stale") {
		t.Fatal(out, e)
	}
}
func TestFREDVintageAndProfileWithholding(t *testing.T) {
	s := testService(t)
	s.Now = func() time.Time { return time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC) }
	s.Getenv = func(k string) string {
		if k == "FRED_API_KEY" {
			return "secret"
		}
		return ""
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("realtime_start") != "2026-01-01" || q.Get("realtime_end") != "2026-01-01" || q.Get("series_id") != "CPIAUCSL" {
			t.Error(q)
		}
		if r.URL.Path == "/series" {
			w.Write([]byte(`{"seriess":[{"title":"CPI","units":"Index","frequency":"Monthly"}]}`))
		} else {
			w.Write([]byte(`{"observations":[{"date":"2025-12-01","value":"100"},{"date":"2026-01-01","value":"102"}]}`))
		}
	}))
	defer server.Close()
	s.Endpoints["fred"] = server.URL
	out, e := s.fred(context.Background(), Request{Indicator: "cpi", CurrentDate: "2026-01-02"})
	if e != nil || !strings.Contains(out, "+2.00") {
		t.Fatal(out, e)
	}
	s.HTTP = &http.Client{Transport: rejectTransport{t}}
	for _, vendor := range []string{"alpha_vantage", "yfinance"} {
		out, e := s.Vendors["get_fundamentals"][vendor](context.Background(), Request{Ticker: "AAPL", CurrentDate: "2025-01-01"})
		if e != nil || !strings.Contains(out, "withheld") {
			t.Fatal(out, e)
		}
	}
	out, e = s.polymarket(context.Background(), Request{CurrentDate: "2025-01-01"})
	if e != nil || !strings.Contains(out, "withheld") {
		t.Fatal(out, e)
	}
}

type rejectTransport struct{ t *testing.T }

func TestPythonVerifiedSnapshot(t *testing.T) {
	b, e := os.ReadFile("testdata/python_snapshot.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Bars         []Bar
		Date, Output string
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	s := testService(t)
	s.HTTP = &http.Client{Transport: rejectTransport{t}}
	// Verification must sort independently even when a cache is out of order.
	slices.Reverse(fixture.Bars)
	raw, e := barsCSV(fixture.Bars, false, -1)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.Config.DataCacheDir, "AAPL-YFin-data.csv"), []byte(raw), 0644); e != nil {
		t.Fatal(e)
	}
	s.Now = time.Now
	got, e := s.Snapshot(context.Background(), "AAPL", fixture.Date, 3)
	if e != nil {
		t.Fatal(e)
	}
	if got != fixture.Output {
		t.Fatalf("snapshot differs\ngot %s\nwant %s", got, fixture.Output)
	}
}

func TestPythonStockCSVPreservesCorporateActions(t *testing.T) {
	b, e := os.ReadFile("testdata/python_stock_csv.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Bars   []Bar
		Output string
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	csv, e := barsCSV(fixture.Bars, true, 2)
	if e != nil {
		t.Fatal(e)
	}
	got := "# Stock data for AAPL from 2025-01-01 to 2025-01-02\n# Total records: 2\n# Data retrieved on: 2026-08-14 12:34:56\n\n" + csv
	// pandas uses the host line ending for CSV; Go emits portable LF.
	if got != strings.ReplaceAll(fixture.Output, "\r\n", "\n") {
		t.Fatalf("got %s\nwant %s", got, fixture.Output)
	}
}

func TestPythonOHLCVGapFillingAndHistoricalCutoff(t *testing.T) {
	b, e := os.ReadFile("testdata/python_gaps.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		CSV, Date string
		Results   map[string][]Bar
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, fill := range []bool{false, true} {
		name := "false"
		if fill {
			name = "true"
		}
		t.Run(name, func(t *testing.T) {
			s := testService(t)
			s.HTTP = &http.Client{Transport: rejectTransport{t}}
			s.Now = time.Now
			if e := os.WriteFile(filepath.Join(s.Config.DataCacheDir, "AAPL-YFin-data.csv"), []byte(fixture.CSV), 0644); e != nil {
				t.Fatal(e)
			}
			got, e := s.LoadOHLCV(context.Background(), "AAPL", fixture.Date, fill)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(got, fixture.Results[name]) {
				a, _ := json.Marshal(got)
				b, _ := json.Marshal(fixture.Results[name])
				t.Fatalf("got %s\nwant %s", a, b)
			}
		})
	}
}
func TestOHLCVInvalidCacheColumns(t *testing.T) {
	for _, csv := range []string{"Open,Close\n100,101\n", "Date,Open\n2026-08-10,100\n"} {
		if _, e := parseBarsCSV([]byte(csv)); e == nil {
			t.Fatalf("accepted incomplete cache: %s", csv)
		}
	}
}

func TestYahooRateLimitBudgetAndCancellation(t *testing.T) {
	for _, recoverAfter := range []int{2, 99} {
		t.Run(fmt.Sprint(recoverAfter), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts <= recoverAfter {
					w.WriteHeader(429)
					return
				}
				w.Write([]byte("ok"))
			}))
			defer server.Close()
			s := testService(t)
			s.Endpoints["yahoo"] = server.URL
			delays := []time.Duration{}
			s.wait = func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return nil }
			b, e := s.yahooGet(context.Background(), "/prices", nil)
			if recoverAfter == 2 {
				if e != nil || string(b) != "ok" || attempts != 3 || !reflect.DeepEqual(delays, []time.Duration{2 * time.Second, 4 * time.Second}) {
					t.Fatal(string(b), e, attempts, delays)
				}
			} else {
				var unavailable *UnavailableError
				if !errors.As(e, &unavailable) || attempts != 4 || !reflect.DeepEqual(delays, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}) {
					t.Fatal(e, attempts, delays)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := waitContext(ctx, time.Hour); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func (r rejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Error("unexpected HTTP request")
	return nil, errors.New("no network")
}
func TestCachedOHLCVExcludesFutureAndUnsettledBars(t *testing.T) {
	s := testService(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	d := now.Format(dateLayout)
	path := filepath.Join(s.Config.DataCacheDir, "AAPL-YFin-data.csv")
	raw := "Date,Open,High,Low,Close,Volume\n" + now.AddDate(0, 0, -1).Format(dateLayout) + ",100,102,99,101,1000\n" + d + ",103,105,102,,1000\n" + now.AddDate(0, 0, 1).Format(dateLayout) + ",110,115,109,111,1000\n"
	if e := os.WriteFile(path, []byte(raw), 0644); e != nil {
		t.Fatal(e)
	}
	s.HTTP = &http.Client{Transport: rejectTransport{t}}
	bars, e := s.LoadOHLCV(context.Background(), "AAPL", d, false)
	if e != nil || len(bars) != 1 || *bars[0].Close != 101 {
		t.Fatal(bars, e)
	}
}
func TestCancelVendor(t *testing.T) {
	s := testService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Route(ctx, "get_stock_data", Request{}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
