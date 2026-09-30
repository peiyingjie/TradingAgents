package dataflows

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Service) alphaRequest(ctx context.Context, function string, q url.Values) (string, error) {
	key := s.Getenv("ALPHA_VANTAGE_API_KEY")
	if key == "" {
		return "", &NotConfiguredError{"ALPHA_VANTAGE_API_KEY environment variable is not set."}
	}
	q.Set("function", function)
	q.Set("apikey", key)
	q.Set("source", "trading_agents")
	b, e := s.request(ctx, "GET", s.Endpoints["alpha_vantage"], q, nil, nil, 30*time.Second)
	if e != nil {
		return "", e
	}
	p, e := jsonObject(b)
	if e == nil {
		notice := text(p["Information"])
		if notice == "" {
			notice = text(p["Note"])
		}
		low := strings.ToLower(notice)
		for _, m := range []string{"rate limit", "requests per day", "call frequency", "premium"} {
			if strings.Contains(low, m) {
				return "", &UnavailableError{"Alpha Vantage rate limit exceeded: " + s.scrub(notice, q)}
			}
		}
		if strings.Contains(low, "api key") || strings.Contains(low, "apikey") {
			return "", &NotConfiguredError{"Alpha Vantage API key invalid or missing: " + s.scrub(notice, q)}
		}
	}
	return string(b), nil
}
func FilterCSV(raw, start, end string) (string, error) {
	rows, e := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if e != nil {
		return "", e
	}
	if len(rows) == 0 {
		return raw, nil
	}
	var out strings.Builder
	w := csv.NewWriter(&out)
	if e = w.Write(rows[0]); e != nil {
		return "", e
	}
	for _, r := range rows[1:] {
		if len(r) == 0 {
			continue
		}
		date := r[0]
		if _, e = ParseDate(date); e != nil {
			return "", e
		}
		if date >= start && date <= end {
			if e = w.Write(r); e != nil {
				return "", e
			}
		}
	}
	w.Flush()
	return out.String(), w.Error()
}
func (s *Service) alpha(ctx context.Context, method string, r Request) (string, error) {
	q := url.Values{}
	function := ""
	ticker := r.Ticker
	if ticker == "" {
		ticker = r.Symbol
	}
	q.Set("symbol", ticker)
	switch method {
	case "get_stock_data":
		start, e := ParseDate(r.StartDate)
		if e != nil {
			return "", e
		}
		size := "full"
		if s.Now().Sub(start).Hours()/24 < 100 {
			size = "compact"
		}
		q.Set("outputsize", size)
		q.Set("datatype", "csv")
		raw, e := s.alphaRequest(ctx, "TIME_SERIES_DAILY_ADJUSTED", q)
		if e != nil {
			return "", e
		}
		return FilterCSV(raw, r.StartDate, r.EndDate)
	case "get_fundamentals":
		if withheld := WithholdProfile(r.CurrentDate, ticker, s.Now().Format(dateLayout)); withheld != "" {
			return withheld, nil
		}
		function = "OVERVIEW"
	case "get_balance_sheet":
		function = "BALANCE_SHEET"
	case "get_cashflow":
		function = "CASH_FLOW"
	case "get_income_statement":
		function = "INCOME_STATEMENT"
	case "get_insider_transactions":
		function = "INSIDER_TRANSACTIONS"
	case "get_news", "get_global_news":
		function = "NEWS_SENTIMENT"
		q.Del("symbol")
		start, end := r.StartDate, r.EndDate
		limit := s.Config.NewsArticleLimit
		if method == "get_news" {
			q.Set("tickers", ticker)
		} else {
			end = r.CurrentDate
			d, e := ParseDate(end)
			if e != nil {
				return "", e
			}
			start = d.AddDate(0, 0, -value(r.LookBackDays, s.Config.GlobalNewsLookbackDays)).Format(dateLayout)
			limit = value(r.Limit, s.Config.GlobalNewsArticleLimit)
			q.Set("topics", "financial_markets,economy_macro,economy_monetary")
		}
		a, e := ParseDate(start)
		if e != nil {
			return "", e
		}
		b, e := ParseDate(end)
		if e != nil {
			return "", e
		}
		q.Set("time_from", a.Format("20060102T0000"))
		q.Set("time_to", b.Format("20060102T2359"))
		q.Set("limit", strconv.Itoa(limit))
	case "get_indicators":
		return s.alphaIndicator(ctx, r)
	default:
		return "", fmt.Errorf("unsupported Alpha Vantage method: %s", method)
	}
	raw, e := s.alphaRequest(ctx, function, q)
	if e != nil {
		return "", e
	}
	if r.CurrentDate != "" && (function == "BALANCE_SHEET" || function == "CASH_FLOW" || function == "INCOME_STATEMENT" || function == "INSIDER_TRANSACTIONS") {
		p, e := jsonObject([]byte(raw))
		if e != nil {
			if function == "INSIDER_TRANSACTIONS" {
				return "", e
			}
			return raw, nil
		}
		keys := []string{"annualReports", "quarterlyReports"}
		field := "fiscalDateEnding"
		if function == "INSIDER_TRANSACTIONS" {
			keys = []string{"data"}
			field = "transaction_date"
		}
		for _, k := range keys {
			if rows, ok := p[k].([]any); ok {
				kept := []any{}
				for _, row := range rows {
					if text(obj(row)[field]) <= r.CurrentDate {
						kept = append(kept, row)
					}
				}
				p[k] = kept
			}
		}
		b, e := json.Marshal(p)
		return string(b), e
	}
	return raw, nil
}
func (s *Service) alphaIndicator(ctx context.Context, r Request) (string, error) {
	q := url.Values{"symbol": {r.Symbol}, "interval": {"daily"}, "datatype": {"csv"}, "series_type": {"close"}}
	f, col := "", ""
	switch r.Indicator {
	case "close_50_sma":
		f, col = "SMA", "SMA"
		q.Set("time_period", "50")
	case "close_200_sma":
		f, col = "SMA", "SMA"
		q.Set("time_period", "200")
	case "close_10_ema":
		f, col = "EMA", "EMA"
		q.Set("time_period", "10")
	case "macd", "macds", "macdh":
		f = "MACD"
		col = map[string]string{"macd": "MACD", "macds": "MACD_Signal", "macdh": "MACD_Hist"}[r.Indicator]
	case "rsi":
		f, col = "RSI", "RSI"
		q.Set("time_period", "14")
	case "atr":
		f, col = "ATR", "ATR"
		q.Set("time_period", "14")
		q.Del("series_type")
	case "boll", "boll_ub", "boll_lb":
		f = "BBANDS"
		q.Set("time_period", "20")
		col = map[string]string{"boll": "Real Middle Band", "boll_ub": "Real Upper Band", "boll_lb": "Real Lower Band"}[r.Indicator]
	default:
		return "", &NoDataError{r.Symbol, r.Symbol, "Alpha Vantage does not serve the " + r.Indicator + " indicator"}
	}
	raw, e := s.alphaRequest(ctx, f, q)
	if e != nil {
		return "", e
	}
	rows, e := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if e != nil {
		return "", &NoDataError{r.Symbol, r.Symbol, e.Error()}
	}
	if len(rows) < 2 {
		return "Error: No data returned for " + r.Indicator, nil
	}
	di, vi := -1, -1
	for i, k := range rows[0] {
		if strings.TrimSpace(k) == "time" {
			di = i
		}
		if strings.TrimSpace(k) == col {
			vi = i
		}
	}
	if di < 0 || vi < 0 {
		return fmt.Sprintf("Error: Column '%s' not found for indicator '%s'. Available columns: %v", col, r.Indicator, rows[0]), nil
	}
	end, e := ParseDate(r.CurrentDate)
	if e != nil {
		return "", e
	}
	start := end.AddDate(0, 0, -value(r.LookBackDays, 30)).Format(dateLayout)
	lines := []string{}
	for _, row := range rows[1:] {
		if len(row) > max(di, vi) && row[di] >= start && row[di] <= r.CurrentDate {
			lines = append(lines, row[di]+": "+row[vi])
		}
	}
	sort.Strings(lines)
	body := strings.Join(lines, "\n") + "\n"
	if len(lines) == 0 {
		body = "No data available for the specified date range.\n"
	}
	return fmt.Sprintf("## %s values from %s to %s:\n\n%s\n\n%s", strings.ToUpper(r.Indicator), start, r.CurrentDate, body, s.Meta.Indicators[r.Indicator]), nil
}
