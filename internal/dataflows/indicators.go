package dataflows

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

func price(v *float64) float64 {
	if v == nil {
		return math.NaN()
	}
	return *v
}
func ewma(v []float64, alpha float64) []float64 {
	out := make([]float64, len(v))
	sum, weight := 0.0, 0.0
	for i, n := range v {
		sum *= 1 - alpha
		weight *= 1 - alpha
		if !math.IsNaN(n) {
			sum += n
			weight++
		}
		out[i] = sum / weight
	}
	return out
}
func rolling(v []float64, window int, std bool) []float64 {
	out := make([]float64, len(v))
	for i := range v {
		sum, n := 0.0, 0
		for j := max(0, i-window+1); j <= i; j++ {
			if !math.IsNaN(v[j]) {
				sum += v[j]
				n++
			}
		}
		mean := sum / float64(n)
		out[i] = mean
		if std {
			ss := 0.0
			for j := max(0, i-window+1); j <= i; j++ {
				if !math.IsNaN(v[j]) {
					ss += (v[j] - mean) * (v[j] - mean)
				}
			}
			out[i] = math.Sqrt(ss / float64(n-1))
		}
	}
	return out
}
func Indicators(bars []Bar) map[string][]float64 {
	n := len(bars)
	close, up, down, tr, tp, flow, volume := make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	for i, b := range bars {
		close[i] = price(b.Close)
		hi, lo := price(b.High), price(b.Low)
		tp[i] = (hi + lo + close[i]) / 3
		volume[i] = price(b.Volume)
		flow[i] = tp[i] * volume[i]
		tr[i] = hi - lo
		if i > 0 {
			d := close[i] - close[i-1]
			up[i] = math.Max(d, 0)
			down[i] = math.Max(-d, 0)
			tr[i] = math.Max(tr[i], math.Max(math.Abs(hi-close[i-1]), math.Abs(lo-close[i-1])))
		}
	}
	out := map[string][]float64{"close_50_sma": rolling(close, 50, false), "close_200_sma": rolling(close, 200, false), "close_10_ema": ewma(close, 2.0/11), "boll": rolling(close, 20, false), "atr": ewma(tr, 1.0/14)}
	fast, slow := ewma(close, 2.0/13), ewma(close, 2.0/27)
	macd := make([]float64, n)
	for i := range macd {
		macd[i] = fast[i] - slow[i]
	}
	signal := ewma(macd, 2.0/10)
	out["macd"], out["macds"] = macd, signal
	for _, k := range []string{"macdh", "rsi", "boll_ub", "boll_lb", "vwma", "mfi"} {
		out[k] = make([]float64, n)
	}
	up, down = ewma(up, 1.0/14), ewma(down, 1.0/14)
	std := rolling(close, 20, true)
	for i := range close {
		out["macdh"][i] = macd[i] - signal[i]
		rsi := 50.0
		if up[i]+down[i] != 0 {
			rsi = 100 * up[i] / (up[i] + down[i])
		}
		out["rsi"][i] = rsi
		out["boll_ub"][i] = out["boll"][i] + 2*std[i]
		out["boll_lb"][i] = out["boll"][i] - 2*std[i]
		weighted, vol := 0.0, 0.0
		for j := max(0, i-13); j <= i; j++ {
			weighted += flow[j]
			vol += volume[j]
		}
		out["vwma"][i] = weighted / vol
		pos, neg := 0.0, 0.0
		for j := max(1, i-13); j <= i; j++ {
			if tp[j] > tp[j-1] {
				pos += flow[j]
			} else if tp[j] < tp[j-1] {
				neg += flow[j]
			}
		}
		mfi := 0.5
		if i >= 14 && pos+neg > 0 {
			mfi = pos / (pos + neg)
		}
		out["mfi"][i] = mfi
	}
	return out
}
func (s *Service) indicator(ctx context.Context, r Request) (string, error) {
	description, ok := s.Meta.Indicators[r.Indicator]
	if !ok {
		return "", fmt.Errorf("indicator %s is not supported", r.Indicator)
	}
	bars, e := s.LoadOHLCV(ctx, r.Symbol, r.CurrentDate, true)
	if e != nil {
		return "", e
	}
	values := Indicators(bars)[r.Indicator]
	byDate := map[string]float64{}
	for i, b := range bars {
		byDate[b.Date] = values[i]
	}
	end, e := ParseDate(r.CurrentDate)
	if e != nil {
		return "", e
	}
	start := end.AddDate(0, 0, -value(r.LookBackDays, 30))
	var lines strings.Builder
	for cursor := end; !cursor.Before(start); cursor = cursor.AddDate(0, 0, -1) {
		date := cursor.Format(dateLayout)
		v := "N/A: Not a trading day (weekend or holiday)"
		if n, ok := byDate[date]; ok {
			v = strconv.FormatFloat(n, 'f', -1, 64)
			if math.IsNaN(n) {
				v = "N/A"
			}
		}
		fmt.Fprintf(&lines, "%s: %s\n", date, v)
	}
	return fmt.Sprintf("## %s values from %s to %s:\n\n%s\n\n%s", r.Indicator, start.Format(dateLayout), r.CurrentDate, lines.String(), description), nil
}
func (s *Service) Snapshot(ctx context.Context, symbol, date string, lookback int) (string, error) {
	bars, e := s.LoadOHLCV(ctx, symbol, date, false)
	if e != nil {
		return "", e
	}
	if len(bars) == 0 {
		return "", fmt.Errorf("no OHLCV rows on or before %s for %s", date, symbol)
	}
	sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	ind := Indicators(bars)
	last := bars[len(bars)-1]
	fmtValue := func(v *float64) string {
		if v == nil || math.IsNaN(*v) {
			return "N/A"
		}
		return fmt.Sprintf("%.2f", *v)
	}
	lines := []string{fmt.Sprintf("## Verified market data snapshot for %s", strings.ToUpper(symbol)), "", "- Requested analysis date: " + date, "- Latest trading row used: " + last.Date, "- Rows after the requested analysis date are excluded before verification.", "", "### Latest verified OHLCV row", "", "| Field | Value |", "|---|---:|"}
	for i, key := range []string{"Open", "High", "Low", "Close", "Volume"} {
		lines = append(lines, fmt.Sprintf("| %s | %s |", key, fmtValue([]*float64{last.Open, last.High, last.Low, last.Close, last.Volume}[i])))
	}
	lines = append(lines, "", "### Verified technical indicators (latest row)", "", "| Indicator | Value |", "|---|---:|")
	for _, name := range []string{"close_10_ema", "close_50_sma", "close_200_sma", "rsi", "boll", "boll_ub", "boll_lb", "macd", "macds", "macdh", "atr"} {
		v := ind[name][len(bars)-1]
		lines = append(lines, fmt.Sprintf("| %s | %s |", name, fmtValue(&v)))
	}
	recent := bars[max(0, len(bars)-max(1, min(lookback, 30))):]
	lines = append(lines, "", fmt.Sprintf("### Recent verified closes (last %d rows)", len(recent)), "", "| Date | Close |", "|---|---:|")
	for _, b := range recent {
		lines = append(lines, fmt.Sprintf("| %s | %s |", b.Date, fmtValue(b.Close)))
	}
	lines = append(lines, "", "Use this snapshot as the source of truth for exact OHLCV, price-level, and indicator-value claims. If another tool output conflicts with it, flag the discrepancy rather than inventing a reconciled number. Do not claim historical validation, support/resistance bounces, or exact percentage moves unless directly supported by tool output with concrete dates and prices.")
	return strings.Join(lines, "\n"), nil
}
