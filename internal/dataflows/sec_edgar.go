package dataflows

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Service) secJSON(ctx context.Context, endpoint, name string) (map[string]any, error) {
	path := filepath.Join(s.Config.DataCacheDir, "sec_edgar", name)
	if st, e := os.Stat(path); e == nil && s.Now().Sub(st.ModTime()) < 24*time.Hour {
		b, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		if p, e := jsonObject(b); e == nil {
			return p, nil
		}
	}
	ua := strings.TrimSpace(s.Getenv("SEC_EDGAR_USER_AGENT"))
	if ua == "" {
		ua = "TradingAgents/dev (contact@example.com)"
	}
	b, e := s.request(ctx, "GET", endpoint, nil, nil, map[string]string{"User-Agent": ua}, 30*time.Second)
	if e != nil {
		return nil, &UnavailableError{"SEC EDGAR request failed (" + e.Error() + ")"}
	}
	p, e := jsonObject(b)
	if e != nil {
		return nil, &UnavailableError{"SEC EDGAR returned an unreadable response"}
	}
	if e = AtomicWrite(path, b); e != nil {
		return nil, e
	}
	return p, nil
}
func (s *Service) sec(ctx context.Context, method string, r Request) (string, error) {
	table, e := s.secJSON(ctx, s.Endpoints["sec"]+"/files/company_tickers.json", "company_tickers.json")
	if e != nil {
		return "", e
	}
	cik := ""
	for _, v := range table {
		entry := obj(v)
		if strings.EqualFold(text(entry["ticker"]), strings.TrimSpace(r.Ticker)) {
			cik = fmt.Sprintf("%010d", int64(number(entry["cik_str"])))
			break
		}
	}
	if cik == "" {
		return "", &NoDataError{r.Ticker, r.Ticker, "not a US SEC filer"}
	}
	p, e := s.secJSON(ctx, s.Endpoints["sec_data"]+"/api/xbrl/companyfacts/CIK"+url.PathEscape(cik)+".json", "CIK"+cik+".json")
	if e != nil {
		return "", e
	}
	facts := obj(obj(p["facts"])["us-gaap"])
	if len(facts) == 0 {
		return "", &NoDataError{r.Ticker, r.Ticker, "US filer with no us-gaap facts"}
	}
	date := r.CurrentDate
	if date == "" {
		date = s.Now().Format(dateLayout)
	}
	kind := strings.TrimPrefix(method, "get_")
	title := map[string]string{"balance_sheet": "Balance Sheet", "income_statement": "Income Statement", "cashflow": "Cash Flow Statement"}[kind]
	low, high := 300, 400
	if strings.EqualFold(r.Frequency, "quarterly") {
		low, high = 60, 115
	}
	type line struct {
		Label, Unit string
		Values      map[string]float64
	}
	lines := []line{}
	periodSet := map[string]bool{}
	for _, raw := range s.Meta.SECStatements[kind] {
		var pair []json.RawMessage
		if e = json.Unmarshal(raw, &pair); e != nil {
			return "", e
		}
		var label string
		var tags []string
		if e = json.Unmarshal(pair[0], &label); e != nil {
			return "", e
		}
		if e = json.Unmarshal(pair[1], &tags); e != nil {
			return "", e
		}
		l := line{Label: label, Unit: "USD", Values: map[string]float64{}}
		for _, tag := range tags {
			for unit, vs := range obj(obj(facts[tag])["units"]) {
				latest := map[string]map[string]any{}
				for _, v := range arr(vs) {
					fact := obj(v)
					end, filed := text(fact["end"]), text(fact["filed"])
					if _, ok := l.Values[end]; ok || filed > date {
						continue
					}
					if start := text(fact["start"]); start != "" {
						a, e := ParseDate(start)
						if e != nil {
							return "", e
						}
						b, e := ParseDate(end)
						if e != nil {
							return "", e
						}
						days := int(b.Sub(a).Hours() / 24)
						if days < low || days > high {
							continue
						}
					}
					if old, ok := latest[end]; !ok || filed >= text(old["filed"]) {
						latest[end] = fact
					}
				}
				if len(latest) > 0 {
					l.Unit = unit
					for end, fact := range latest {
						l.Values[end] = number(fact["val"])
						periodSet[end] = true
					}
				}
			}
		}
		lines = append(lines, l)
	}
	periods := []string{}
	for p := range periodSet {
		periods = append(periods, p)
	}
	sort.Strings(periods)
	if len(periods) == 0 {
		return "", &NoDataError{r.Ticker, r.Ticker, fmt.Sprintf("no %s %s filed by %s", r.Frequency, strings.ToLower(title), date)}
	}
	rows := []string{"," + strings.Join(periods, ",")}
	for _, l := range lines {
		label := l.Label
		if l.Unit != "USD" {
			label += " (" + l.Unit + ")"
		}
		cells := []string{label}
		for _, p := range periods {
			v, ok := l.Values[p]
			cell := ""
			if len(l.Values) == 0 {
				cell = "unavailable (not tagged by this filer)"
			} else if ok {
				if l.Unit == "USD" {
					cell = fmt.Sprintf("%.0f", v/1e6)
				} else {
					cell = fmt.Sprintf("%.2f", v)
				}
			}
			cells = append(cells, cell)
		}
		rows = append(rows, strings.Join(cells, ","))
	}
	return fmt.Sprintf("# %s for %s (%s), USD in millions unless the row says otherwise\n# SEC EDGAR facts filed on or before %s, at the values filed then\n\n%s\n", title, strings.ToUpper(r.Ticker), r.Frequency, date, strings.Join(rows, "\n")), nil
}
