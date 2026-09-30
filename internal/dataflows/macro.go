package dataflows

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

func (s *Service) fred(ctx context.Context, r Request) (string, error) {
	end, e := ParseDate(r.CurrentDate)
	if e != nil {
		return "", e
	}
	start := end.AddDate(0, 0, -value(r.LookBackDays, 365)).Format(dateLayout)
	id := strings.TrimSpace(r.Indicator)
	alias := strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToLower(id))
	if mapped, ok := s.Meta.Macro[alias]; ok {
		id = mapped
	} else {
		id = strings.ToUpper(id)
		if id == "" || len(id) > 30 || strings.ContainsAny(id, " \t\n") {
			return fmt.Sprintf("FRED: '%s' is not a known macro alias or a valid FRED series ID. Use an alias (e.g. 'cpi', 'unemployment', '10y_treasury') or a raw FRED series ID (e.g. 'CPIAUCSL').", r.Indicator), nil
		}
	}
	key := s.Getenv("FRED_API_KEY")
	if key == "" {
		return "", &NotConfiguredError{"FRED_API_KEY environment variable is not set. Get a free key at https://fred.stlouisfed.org/docs/api/api_key.html."}
	}
	loc, e := time.LoadLocation("America/Chicago")
	if e != nil {
		return "", e
	}
	pit := min(r.CurrentDate, s.Now().In(loc).Format(dateLayout))
	request := func(path string, q url.Values) (map[string]any, error) {
		q.Set("series_id", id)
		q.Set("realtime_start", pit)
		q.Set("realtime_end", pit)
		q.Set("api_key", key)
		q.Set("file_type", "json")
		b, e := s.request(ctx, "GET", s.Endpoints["fred"]+"/"+path, q, nil, nil, 30*time.Second)
		if e != nil {
			return nil, e
		}
		return jsonObject(b)
	}
	meta, e := request("series", url.Values{})
	if e != nil {
		return "", e
	}
	series := arr(meta["seriess"])
	if len(series) == 0 {
		return fmt.Sprintf("FRED series '%s' not found. Pass a known alias (e.g. 'cpi', 'unemployment') or a valid FRED series ID.", id), nil
	}
	info := obj(series[0])
	title := text(info["title"])
	if title == "" {
		title = id
	}
	units := text(info["units_short"])
	if units == "" {
		units = text(info["units"])
	}
	seasonal := text(info["seasonal_adjustment_short"])
	if seasonal != "" {
		seasonal = " (" + seasonal + ")"
	}
	header := fmt.Sprintf("## FRED: %s (%s)\n- Units: %s\n- Frequency: %s%s\n- Window: %s to %s\n", title, id, units, text(info["frequency"]), seasonal, start, r.CurrentDate)
	obs, e := request("series/observations", url.Values{"observation_start": {start}, "observation_end": {r.CurrentDate}, "sort_order": {"asc"}})
	if e != nil {
		return "", e
	}
	points := [][2]string{}
	for _, v := range arr(obs["observations"]) {
		o := obj(v)
		value := text(o["value"])
		if value != "." && value != "" {
			points = append(points, [2]string{text(o["date"]), value})
		}
	}
	if len(points) == 0 {
		return header + fmt.Sprintf("\nNo observations for %s in this window at the %s vintage. The series may report less frequently than the window (try a longer look_back_days), or have no vintage published by then (unpublished as of %s, or before ALFRED coverage begins).", id, pit, pit), nil
	}
	first, last := points[0], points[len(points)-1]
	summary := fmt.Sprintf("\n**Latest:** %s (%s)\n", last[1], last[0])
	fv, fe := strconv.ParseFloat(first[1], 64)
	lv, le := strconv.ParseFloat(last[1], 64)
	if fe == nil && le == nil {
		pct := ""
		if fv != 0 {
			pct = fmt.Sprintf(" (%+.2f%%)", (lv-fv)/fv*100)
		}
		summary = fmt.Sprintf("\n**Latest:** %s (%s) | **Change over window:** %+.2f%s from %s (%s)\n", last[1], last[0], lv-fv, pct, first[1], first[0])
	}
	note := ""
	if len(points) > 40 {
		note = fmt.Sprintf("\n_(showing the most recent 40 of %d observations)_\n", len(points))
		points = points[len(points)-40:]
	}
	lines := []string{}
	for _, p := range points {
		lines = append(lines, fmt.Sprintf("| %s | %s |", p[0], p[1]))
	}
	return header + summary + note + "\n| Date | Value |\n| --- | --- |\n" + strings.Join(lines, "\n") + "\n", nil
}
func listJSON(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if text, ok := v.(string); ok {
		var a []any
		if json.Unmarshal([]byte(text), &a) == nil {
			return a
		}
	}
	return nil
}
func commaNumber(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	for i := len(s) - 3; i > 0; i -= 3 {
		if s[i-1] == '-' {
			break
		}
		s = s[:i] + "," + s[i:]
	}
	return s
}
func (s *Service) polymarket(ctx context.Context, r Request) (string, error) {
	if r.CurrentDate != "" && r.CurrentDate < s.Now().Format(dateLayout) {
		return fmt.Sprintf("Prediction-market odds are withheld for %s. Polymarket serves only live odds on open markets, with no historical vintage, so serving them would put post-decision information into a %s analysis.", r.CurrentDate, r.CurrentDate), nil
	}
	b, e := s.request(ctx, "GET", s.Endpoints["polymarket"]+"/public-search", url.Values{"q": {r.Topic}, "limit_per_type": {"20"}}, nil, nil, 30*time.Second)
	if e != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return fmt.Sprintf("Polymarket data is currently unavailable (network error: %s). Proceed without prediction-market signal for '%s'.", e, r.Topic), nil
	}
	p, e := jsonObject(b)
	if e != nil {
		return "", e
	}
	candidates := []map[string]any{}
	for _, event := range arr(p["events"]) {
		for _, v := range arr(obj(event)["markets"]) {
			m := obj(v)
			if m["closed"] == true {
				continue
			}
			if end, e := time.Parse(time.RFC3339, text(m["endDate"])); e == nil && end.Before(s.Now()) {
				continue
			}
			if len(listJSON(m["outcomePrices"])) > 0 && len(listJSON(m["outcomes"])) > 0 {
				candidates = append(candidates, m)
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return number(candidates[i]["volumeNum"]) > number(candidates[j]["volumeNum"]) })
	header := fmt.Sprintf("## Polymarket prediction markets: %q\nLive, market-implied probabilities (higher traded volume = deeper, more reliable). A probability is the crowd's priced odds of the event, not a forecast you should take as certain.\n\n", r.Topic)
	if len(candidates) == 0 {
		return header + fmt.Sprintf("No open prediction markets matched '%s'. Polymarket coverage is concentrated in macro, political, geopolitical, and crypto events; a specific equity may have none.", r.Topic), nil
	}
	lines := []string{}
	for _, m := range candidates[:max(0, min(len(candidates), value(r.Limit, 6)))] {
		prob, e := strconv.ParseFloat(text(listJSON(m["outcomePrices"])[0]), 64)
		if e != nil {
			continue
		}
		end := text(m["endDate"])
		if len(end) > 10 {
			end = end[:10]
		}
		week := ""
		if wk := number(m["oneWeekPriceChange"]); wk != 0 {
			week = fmt.Sprintf(", 1-week %+.1fpp", wk*100)
		}
		lines = append(lines, fmt.Sprintf("- **%s** — %s %.0f%% ($%s volume, resolves %s%s)", text(m["question"]), text(listJSON(m["outcomes"])[0]), prob*100, commaNumber(number(m["volumeNum"])), end, week))
	}
	return header + strings.Join(lines, "\n") + "\n", nil
}
