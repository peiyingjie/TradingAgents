package dataflows

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"tradingagents/internal/config"
	"tradingagents/internal/tools"
	"tradingagents/pkg/model"
)

//go:embed metadata.json
var metadataJSON []byte

type metadata struct {
	Aliases            map[string]string            `json:"aliases"`
	Forex              []string                     `json:"forex"`
	Crypto             []string                     `json:"crypto"`
	Macro              map[string]string            `json:"macro"`
	Indicators         map[string]string            `json:"indicators"`
	SECStatements      map[string][]json.RawMessage `json:"sec_statements"`
	YahooFinancialKeys map[string][]string          `json:"yahoo_financial_keys"`
}
type Request struct {
	Symbol, Ticker, StartDate, EndDate, CurrentDate, Indicator, Frequency, Topic string
	LookBackDays, Limit                                                          *int
}
type VendorFunc func(context.Context, Request) (string, error)
type Service struct {
	Config    config.Config
	HTTP      *http.Client
	Now       func() time.Time
	Getenv    func(string) string
	Meta      metadata
	Endpoints map[string]string
	Vendors   map[string]map[string]VendorFunc
	mu        sync.Mutex
	crumb     string
	wait      func(context.Context, time.Duration) error
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func New(c config.Config) (*Service, error) {
	jar, e := cookiejar.New(nil)
	if e != nil {
		return nil, e
	}
	s := &Service{Config: c, HTTP: &http.Client{Timeout: 30 * time.Second, Jar: jar}, Now: time.Now, Getenv: os.Getenv, Endpoints: map[string]string{"yahoo": "https://query2.finance.yahoo.com", "yahoo_news": "https://query1.finance.yahoo.com", "alpha_vantage": "https://www.alphavantage.co/query", "fred": "https://api.stlouisfed.org/fred", "polymarket": "https://gamma-api.polymarket.com", "stocktwits": "https://api.stocktwits.com", "reddit": "https://www.reddit.com", "sec": "https://www.sec.gov", "sec_data": "https://data.sec.gov"}}
	if e = json.Unmarshal(metadataJSON, &s.Meta); e != nil {
		return nil, e
	}
	s.Vendors = map[string]map[string]VendorFunc{}
	for method, vs := range vendorOrder {
		m := method
		s.Vendors[m] = map[string]VendorFunc{}
		for _, vendor := range vs {
			v := vendor
			s.Vendors[m][v] = func(ctx context.Context, r Request) (string, error) {
				switch v {
				case "yfinance":
					return s.yahoo(ctx, m, r)
				case "alpha_vantage":
					return s.alpha(ctx, m, r)
				case "fred":
					return s.fred(ctx, r)
				case "polymarket":
					return s.polymarket(ctx, r)
				case "sec_edgar":
					return s.sec(ctx, m, r)
				}
				return "", fmt.Errorf("unknown vendor %s", v)
			}
		}
	}
	return s, nil
}

type NoDataError struct{ Symbol, Canonical, Detail string }

func (e *NoDataError) Error() string {
	return fmt.Sprintf("no market data for %s: %s", e.Symbol, e.Detail)
}

type UnavailableError struct{ Detail string }

func (e *UnavailableError) Error() string { return e.Detail }

type NotConfiguredError struct{ Detail string }

func (e *NotConfiguredError) Error() string { return e.Detail }

var category = map[string]string{"get_stock_data": "core_stock_apis", "get_indicators": "technical_indicators", "get_fundamentals": "fundamental_data", "get_balance_sheet": "fundamental_data", "get_cashflow": "fundamental_data", "get_income_statement": "fundamental_data", "get_news": "news_data", "get_global_news": "news_data", "get_insider_transactions": "news_data", "get_macro_indicators": "macro_data", "get_prediction_markets": "prediction_markets"}
var vendorOrder = map[string][]string{"get_stock_data": {"alpha_vantage", "yfinance"}, "get_indicators": {"alpha_vantage", "yfinance"}, "get_fundamentals": {"alpha_vantage", "yfinance"}, "get_balance_sheet": {"alpha_vantage", "sec_edgar", "yfinance"}, "get_cashflow": {"alpha_vantage", "sec_edgar", "yfinance"}, "get_income_statement": {"alpha_vantage", "sec_edgar", "yfinance"}, "get_news": {"alpha_vantage", "yfinance"}, "get_global_news": {"yfinance", "alpha_vantage"}, "get_insider_transactions": {"alpha_vantage", "yfinance"}, "get_macro_indicators": {"fred"}, "get_prediction_markets": {"polymarket"}}

func (s *Service) Route(ctx context.Context, method string, r Request) (string, error) {
	cat, ok := category[method]
	if !ok {
		return "", fmt.Errorf("method %q not found in any category", method)
	}
	configured, ok := s.Config.ToolVendors[method]
	if !ok {
		configured = s.Config.DataVendors[cat]
	}
	chain := []string{}
	explicit := []string{}
	for _, v := range strings.Split(configured, ",") {
		v = strings.TrimSpace(v)
		if v != "" && v != "default" {
			explicit = append(explicit, v)
			if s.Vendors[method][v] != nil {
				chain = append(chain, v)
			}
		}
	}
	if len(explicit) == 0 {
		chain = vendorOrder[method]
	} else if len(chain) == 0 {
		return "", fmt.Errorf("configured vendors %v not available for %s; available: %v", explicit, method, vendorOrder[method])
	}
	var first error
	var noData *NoDataError
	var unavailable *UnavailableError
	for _, v := range chain {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		result, e := s.Vendors[method][v](ctx, r)
		if e == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		slog.Warn("data vendor failed", "vendor", v, "method", method, "error", e)
		var nd *NoDataError
		var ua *UnavailableError
		switch {
		case errors.As(e, &nd):
			noData = nd
		case errors.As(e, &ua):
			unavailable = ua
		default:
			if first == nil {
				first = e
			}
		}
	}
	if noData != nil {
		resolved, reason := "", ""
		if noData.Canonical != "" && noData.Canonical != noData.Symbol {
			resolved = fmt.Sprintf(" (resolved to '%s')", noData.Canonical)
		}
		if noData.Detail != "" {
			reason = " (" + noData.Detail + ")"
		}
		return fmt.Sprintf("NO_DATA_AVAILABLE: No usable market data for '%s'%s from any configured vendor%s. The symbol may be invalid, delisted, not covered, or the vendor returned stale data. Do not estimate or fabricate values — report that data is unavailable for this symbol.", noData.Symbol, resolved, reason), nil
	}
	if unavailable != nil {
		return fmt.Sprintf("DATA_UNAVAILABLE: no configured vendor could serve %s right now (%s). This says nothing about the instrument; report the data as unavailable and do not estimate or fabricate values.", method, unavailable), nil
	}
	if first != nil {
		if cat == "macro_data" || cat == "prediction_markets" {
			return fmt.Sprintf("DATA_UNAVAILABLE: optional %s could not be retrieved (%s). Proceed without it; do not fabricate values.", cat, first), nil
		}
		return "", first
	}
	return "", fmt.Errorf("no available vendor for %q", method)
}

type HTTPError struct {
	Status     int
	Body       string
	RetryAfter string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }
func (s *Service) request(ctx context.Context, method, endpoint string, q url.Values, body any, headers map[string]string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u, e := url.Parse(endpoint)
	if e != nil {
		return nil, e
	}
	if q != nil {
		u.RawQuery = q.Encode()
	}
	var data []byte
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil {
			return nil, e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := s.HTTP.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("request to %s failed: %s", u.Host, s.scrub(e.Error(), q))
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	ce := resp.Body.Close()
	if e != nil {
		return nil, e
	}
	if ce != nil {
		return nil, ce
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{resp.StatusCode, s.scrub(string(b), q), resp.Header.Get("Retry-After")}
	}
	return b, nil
}
func (s *Service) scrub(text string, q url.Values) string {
	for _, key := range []string{"api_key", "apikey", "crumb"} {
		if v := q.Get(key); v != "" {
			text = strings.ReplaceAll(text, v, "[REDACTED]")
		}
	}
	return text
}
func jsonObject(b []byte) (map[string]any, error) {
	var out map[string]any
	e := json.Unmarshal(b, &out)
	return out, e
}
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }
func text(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}
func value(n *int, fallback int) int {
	if n == nil {
		return fallback
	}
	return *n
}
func (s *Service) Registry() (*tools.Registry, error) {
	specs, e := tools.Specs()
	if e != nil {
		return nil, e
	}
	items := []tools.Tool{}
	for name, spec := range specs {
		n := name
		items = append(items, tools.Function{Definition: spec, Inject: map[string]string{"trade_date": "trade_date"}, Run: func(ctx context.Context, a map[string]any) (model.Content, error) {
			r := Request{Symbol: text(a["symbol"]), Ticker: text(a["ticker"]), StartDate: text(a["start_date"]), EndDate: text(a["end_date"]), CurrentDate: AsOf(text(a["curr_date"]), text(a["trade_date"])), Frequency: text(a["freq"]), Indicator: text(a["indicator"]), Topic: text(a["topic"])}
			if r.Frequency == "" {
				r.Frequency = "quarterly"
			}
			if v, ok := a["look_back_days"]; ok && v != nil {
				i := int(number(v))
				r.LookBackDays = &i
			}
			if v, ok := a["limit"]; ok && v != nil {
				i := int(number(v))
				r.Limit = &i
			}
			r.StartDate, r.EndDate = AsOfWindow(r.StartDate, r.EndDate, text(a["trade_date"]))
			var result string
			var err error
			if n == "get_verified_market_snapshot" {
				result, err = s.Snapshot(ctx, r.Symbol, r.CurrentDate, value(r.LookBackDays, 30))
			} else if n == "get_indicators" {
				parts := []string{}
				for _, ind := range strings.Split(r.Indicator, ",") {
					ind = strings.ToLower(strings.TrimSpace(ind))
					if ind == "" {
						continue
					}
					r.Indicator = ind
					part, e := s.Route(ctx, n, r)
					if e != nil {
						return model.Content{}, e
					}
					parts = append(parts, part)
				}
				result = strings.Join(parts, "\n\n")
			} else {
				result, err = s.Route(ctx, n, r)
			}
			return model.Text(result), err
		}})
	}
	return tools.NewRegistry(items)
}
func (s *Service) News(ctx context.Context, ticker, start, end string) (string, error) {
	return s.Route(ctx, "get_news", Request{Ticker: ticker, StartDate: start, EndDate: end})
}
func SafeComponent(v string) (string, error) {
	if v == "" || len(v) > 32 || strings.Trim(v, ".") == "" || !regexp.MustCompile(`^[A-Za-z0-9._\-\^=+]+$`).MatchString(v) {
		return "", fmt.Errorf("invalid ticker/path component: %q", v)
	}
	return v, nil
}
