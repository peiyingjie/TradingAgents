package dataflows

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const periodVintage = "# Periods are cut at the fiscal period end; this vendor does not report filing dates, so the most recent period may not have been published yet.\n\n"
const transactionVintage = "# Rows are dated by transaction date. A trade becomes public when its Form 4 is filed, up to two business days later, so the newest rows may not have been known on this date.\n\n"

func prettyField(s string) string {
	a := regexp.MustCompile(`([a-z0-9])([A-Z])`).ReplaceAllString(s, "$1 $2")
	return regexp.MustCompile(`([A-Z])([A-Z][a-z])`).ReplaceAllString(a, "$1 $2")
}
func (s *Service) yahooStatement(ctx context.Context, method string, r Request) (string, error) {
	kind, title := "financials", "Income Statement"
	if method == "get_balance_sheet" {
		kind, title = "balance-sheet", "Balance Sheet"
	}
	if method == "get_cashflow" {
		kind, title = "cash-flow", "Cash Flow"
	}
	keys := s.Meta.YahooFinancialKeys[kind]
	if len(keys) == 0 {
		return "", fmt.Errorf("missing Yahoo statement key catalog: %s", kind)
	}
	freq := "annual"
	if strings.EqualFold(r.Frequency, "quarterly") {
		freq = "quarterly"
	}
	canonical := s.Normalize(r.Ticker)
	start := time.Date(2016, 12, 31, 0, 0, 0, 0, time.Local)
	now := s.Now().UTC()
	end := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	types := []string{}
	for _, k := range keys {
		types = append(types, freq+k)
	}
	fetch := func(types []string) ([]any, error) {
		q := url.Values{"symbol": {canonical}, "type": {strings.Join(types, ",")}, "period1": {strconv.FormatInt(start.Unix(), 10)}, "period2": {strconv.FormatInt(end.Unix(), 10)}}
		b, e := s.yahooGet(ctx, "/ws/fundamentals-timeseries/v1/finance/timeseries/"+url.PathEscape(canonical), q)
		if e != nil {
			return nil, e
		}
		p, e := jsonObject(b)
		if e != nil {
			return nil, e
		}
		result := arr(obj(p["timeseries"])["result"])
		if len(result) == 0 {
			return nil, fmt.Errorf("empty fundamentals-timeseries result")
		}
		return result, nil
	}
	result, e := fetch(types)
	if e != nil {
		result = nil
		for i := 0; i < len(types); i += 60 {
			chunk, err := fetch(types[i:min(i+60, len(types))])
			if err != nil {
				return "", &NoDataError{r.Ticker, canonical, title + " unavailable: " + err.Error()}
			}
			result = append(result, chunk...)
		}
	}
	values := map[string]map[string]float64{}
	periods := map[string]bool{}
	for _, v := range result {
		for k, data := range obj(v) {
			if !strings.HasPrefix(k, freq) {
				continue
			}
			field := strings.TrimPrefix(k, freq)
			for _, point := range arr(data) {
				p := obj(point)
				date := text(p["asOfDate"])
				if r.CurrentDate != "" && date > r.CurrentDate {
					continue
				}
				if values[field] == nil {
					values[field] = map[string]float64{}
				}
				values[field][date] = number(obj(p["reportedValue"])["raw"])
				periods[date] = true
			}
		}
	}
	if len(periods) == 0 {
		return "", &NoDataError{r.Ticker, canonical, "no " + strings.ToLower(title) + " data"}
	}
	dates := []string{}
	for d := range periods {
		dates = append(dates, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	var out strings.Builder
	w := csv.NewWriter(&out)
	if e = w.Write(append([]string{""}, dates...)); e != nil {
		return "", e
	}
	for _, k := range keys {
		vs, ok := values[k]
		if !ok {
			continue
		}
		row := []string{prettyField(k)}
		for _, d := range dates {
			v, ok := vs[d]
			cell := ""
			if ok {
				cell = strconv.FormatFloat(v, 'f', 1, 64)
			}
			row = append(row, cell)
		}
		if e = w.Write(row); e != nil {
			return "", e
		}
	}
	w.Flush()
	if e = w.Error(); e != nil {
		return "", e
	}
	return fmt.Sprintf("# %s data for %s (%s)\n# Data retrieved on: %s\n%s%s", title, canonical, r.Frequency, s.Now().Format("2006-01-02 15:04:05"), periodVintage, out.String()), nil
}
func (s *Service) yahooInsiders(ctx context.Context, r Request) (string, error) {
	canonical := s.Normalize(r.Ticker)
	b, e := s.yahooGet(ctx, "/v10/finance/quoteSummary/"+url.PathEscape(canonical), url.Values{"modules": {"insiderTransactions"}, "formatted": {"false"}})
	if e != nil {
		return "", &NoDataError{r.Ticker, canonical, "insider transactions unavailable: " + e.Error()}
	}
	p, e := jsonObject(b)
	if e != nil {
		return "", e
	}
	results := arr(obj(p["quoteSummary"])["result"])
	rows := []any{}
	if len(results) > 0 {
		rows = arr(obj(obj(results[0])["insiderTransactions"])["transactions"])
	}
	if len(rows) == 0 {
		return fmt.Sprintf("No insider transactions reported for symbol '%s'", canonical), nil
	}
	var out strings.Builder
	w := csv.NewWriter(&out)
	if e = w.Write([]string{"", "Shares", "Value", "URL", "Text", "Insider", "Position", "Transaction", "Start Date", "Ownership"}); e != nil {
		return "", e
	}
	oldest := "9999-12-31"
	count := 0
	for i, v := range rows {
		row := obj(v)
		dateVal := row["startDate"]
		if raw, ok := obj(dateVal)["raw"]; ok {
			dateVal = raw
		}
		date := time.Unix(int64(number(dateVal)), 0).UTC().Format(dateLayout)
		oldest = min(oldest, date)
		if r.CurrentDate != "" && date > r.CurrentDate {
			continue
		}
		value := func(key string) string {
			v := row[key]
			if raw, ok := obj(v)["raw"]; ok {
				v = raw
			}
			return text(v)
		}
		record := []string{strconv.Itoa(i), value("shares"), value("value"), value("url"), value("transactionText"), value("filerName"), value("filerRelation"), value("moneyText"), date, value("ownership")}
		if e = w.Write(record); e != nil {
			return "", e
		}
		count++
	}
	if count == 0 {
		return fmt.Sprintf("<insider transactions unavailable for %s as of %s: Yahoo serves recent transactions only (coverage starts %s)>", canonical, r.CurrentDate, oldest), nil
	}
	w.Flush()
	if e = w.Error(); e != nil {
		return "", e
	}
	return fmt.Sprintf("# Insider Transactions data for %s\n# Data retrieved on: %s\n%s%s", canonical, s.Now().Format("2006-01-02 15:04:05"), transactionVintage, out.String()), nil
}
