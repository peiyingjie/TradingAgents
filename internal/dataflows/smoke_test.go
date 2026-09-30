package dataflows

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type loopbackOnly struct {
	base http.RoundTripper
	host string
}

func (t loopbackOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != t.host {
		return nil, fmt.Errorf("smoke blocked external network: %s", r.URL.Host)
	}
	return t.base.RoundTrip(r)
}

func TestSmokeAllDataVendorRoutes(t *testing.T) {
	s := testService(t)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	s.Getenv = func(k string) string {
		switch k {
		case "FRED_API_KEY", "ALPHA_VANTAGE_API_KEY":
			return "smoke-key"
		case "SEC_EDGAR_USER_AGENT":
			return "Smoke test test@example.invalid"
		}
		return ""
	}
	facts := map[string]any{}
	for _, lines := range s.Meta.SECStatements {
		for _, raw := range lines {
			var pair []json.RawMessage
			if e := json.Unmarshal(raw, &pair); e != nil {
				t.Fatal(e)
			}
			var tags []string
			if e := json.Unmarshal(pair[1], &tags); e != nil {
				t.Fatal(e)
			}
			for _, tag := range tags {
				facts[tag] = map[string]any{"units": map[string]any{"USD": []any{map[string]any{"val": 100, "start": "2025-01-01", "end": "2025-12-31", "filed": "2026-02-01", "form": "10-K", "fp": "FY"}}}}
			}
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload any
		switch {
		case r.URL.Path == "/alpha":
			if r.URL.Query().Get("apikey") != "smoke-key" {
				t.Error("non-test Alpha Vantage credential")
			}
			switch r.URL.Query().Get("function") {
			case "TIME_SERIES_DAILY_ADJUSTED":
				w.Write([]byte("timestamp,open,high,low,close,volume\n2026-08-14,100,102,99,101,1000\n"))
				return
			case "RSI":
				w.Write([]byte("time,RSI\n2026-08-14,50\n"))
				return
			case "NEWS_SENTIMENT":
				payload = map[string]any{"feed": []any{map[string]any{"title": "Smoke news", "time_published": "20260814T100000"}}}
			default:
				payload = map[string]any{"Name": "Smoke company", "annualReports": []any{map[string]any{"fiscalDateEnding": "2025-12-31", "value": "100"}}, "data": []any{map[string]any{"transaction_date": "2026-08-13"}}}
			}
		case strings.HasPrefix(r.URL.Path, "/v8/finance/chart/"):
			quote := map[string]any{"open": []int{100}, "high": []int{102}, "low": []int{99}, "close": []int{101}, "volume": []int{1000}}
			result := map[string]any{"meta": map[string]any{"exchangeTimezoneName": "UTC"}, "timestamp": []int64{now.Unix()}, "indicators": map[string]any{"quote": []any{quote}}}
			payload = map[string]any{"chart": map[string]any{"result": []any{result}}}
		case strings.HasPrefix(r.URL.Path, "/v10/finance/quoteSummary/"):
			payload = map[string]any{"quoteSummary": map[string]any{"result": []any{map[string]any{"quoteType": map[string]any{"longName": "Smoke company"}, "insiderTransactions": map[string]any{"transactions": []any{}}}}}}
		case strings.HasPrefix(r.URL.Path, "/ws/fundamentals-timeseries/"):
			key := strings.Split(r.URL.Query().Get("type"), ",")[0]
			payload = map[string]any{"timeseries": map[string]any{"result": []any{map[string]any{key: []any{map[string]any{"asOfDate": "2025-12-31", "reportedValue": map[string]any{"raw": 100}}}}}}}
		case r.URL.Path == "/xhr/ncp":
			payload = map[string]any{"data": map[string]any{"tickerStream": map[string]any{"stream": []any{map[string]any{"content": map[string]any{"title": "Smoke news", "pubDate": "2026-08-14T10:00:00Z"}}}}}}
		case r.URL.Path == "/v1/finance/search":
			payload = map[string]any{"news": []any{map[string]any{"title": "Smoke global news", "providerPublishTime": now.Unix()}}}
		case r.URL.Path == "/series":
			payload = map[string]any{"seriess": []any{map[string]any{"title": "Smoke series", "units": "Percent", "frequency": "Daily"}}}
		case r.URL.Path == "/series/observations":
			payload = map[string]any{"observations": []any{map[string]any{"date": "2026-08-14", "value": "3.0"}}}
		case r.URL.Path == "/public-search":
			payload = map[string]any{"events": []any{map[string]any{"markets": []any{map[string]any{"question": "Smoke outcome?", "endDate": "2027-01-01T00:00:00Z", "outcomes": `["Yes","No"]`, "outcomePrices": `["0.6","0.4"]`, "volumeNum": 1000}}}}}
		case r.URL.Path == "/files/company_tickers.json":
			payload = map[string]any{"0": map[string]any{"ticker": "AAPL", "cik_str": 320193}}
		case strings.HasPrefix(r.URL.Path, "/api/xbrl/companyfacts/"):
			payload = map[string]any{"facts": map[string]any{"us-gaap": facts}}
		case strings.HasPrefix(r.URL.Path, "/api/2/streams/symbol/"):
			payload = map[string]any{"messages": []any{map[string]any{"body": "Smoke social evidence", "created_at": "2026-08-14T10:00:00Z", "entities": map[string]any{"sentiment": map[string]any{"basic": "Bullish"}}}}}
		case strings.HasSuffix(r.URL.Path, "/search.rss"):
			w.Write([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Smoke Reddit evidence</title><published>2026-08-14T10:00:00Z</published><category term="stocks"/><content type="html">Evidence</content></entry></feed>`))
			return
		default:
			t.Errorf("unhandled mock route %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if e := json.NewEncoder(w).Encode(payload); e != nil {
			t.Error(e)
		}
	}))
	defer server.Close()
	for key := range s.Endpoints {
		s.Endpoints[key] = server.URL
	}
	s.Endpoints["yahoo_site"] = server.URL
	s.Endpoints["alpha_vantage"] = server.URL + "/alpha"
	s.HTTP = &http.Client{Transport: loopbackOnly{server.Client().Transport, strings.TrimPrefix(server.URL, "http://")}}
	req := Request{Ticker: "AAPL", Symbol: "AAPL", StartDate: "2026-08-13", EndDate: "2026-08-14", CurrentDate: "2026-08-14", Frequency: "annual", Indicator: "rsi", Topic: "smoke"}
	for method, vendors := range s.Vendors {
		for name, vendor := range vendors {
			t.Run(name+"/"+method, func(t *testing.T) {
				output, e := vendor(context.Background(), req)
				if e != nil {
					t.Fatal(e)
				}
				if strings.TrimSpace(output) == "" || strings.Contains(output, "unavailable") || strings.HasPrefix(output, "Error:") {
					t.Fatalf("vendor returned failure: %s", output)
				}
			})
		}
	}
	for name, call := range map[string]func() (string, error){
		"StockTwits":        func() (string, error) { return s.StockTwits(context.Background(), "AAPL", req.StartDate, req.EndDate) },
		"Reddit":            func() (string, error) { return s.Reddit(context.Background(), "AAPL", req.StartDate, req.EndDate) },
		"verified_snapshot": func() (string, error) { return s.Snapshot(context.Background(), "AAPL", req.CurrentDate, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			out, e := call()
			if e != nil || strings.Contains(out, "unavailable") || out == "" {
				t.Fatal(out, e)
			}
		})
	}
}
