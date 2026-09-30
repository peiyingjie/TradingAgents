package backtest

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"tradingagents/internal/config"
	"tradingagents/internal/dataflows"
	"tradingagents/internal/graph"
	"tradingagents/internal/memory"
	"tradingagents/internal/portfolio"
	"tradingagents/internal/state"
)

func Grid(start, end string, every int, now time.Time) ([]string, error) {
	a, e := dataflows.ParseDate(start)
	if e != nil {
		return nil, e
	}
	b, e := dataflows.ParseDate(end)
	if e != nil {
		return nil, e
	}
	if every < 1 {
		return nil, fmt.Errorf("every_n_days must be at least 1")
	}
	if b.Before(a) {
		return nil, fmt.Errorf("the grid ends before it starts: %s is before %s", end, start)
	}
	today, e := dataflows.ParseDate(now.Format("2006-01-02"))
	if e != nil {
		return nil, e
	}
	if b.After(today) {
		b = today
	}
	dates := []string{}
	for d := a; !d.After(b); d = d.AddDate(0, 0, every) {
		dates = append(dates, d.Format("2006-01-02"))
	}
	return dates, nil
}

type Failure struct{ Ticker, Date, Reason string }
type Result struct {
	RunID, LogPath               string
	CellsRun, Skipped            int
	Failures, SettlementFailures []Failure
}
type Runner interface {
	Propagate(context.Context, string, string, string, *portfolio.Portfolio) (state.State, string, error)
	SettlePending(context.Context, string) error
}
type Factory func(config.Config, []string) (Runner, error)

func Run(ctx context.Context, tickers, dates []string, c config.Config, asset string, book *portfolio.Portfolio, selected []string, runID string) (Result, error) {
	return RunWithFactory(ctx, tickers, dates, c, asset, book, selected, runID, func(c config.Config, a []string) (Runner, error) { return graph.New(c, a) }, time.Now())
}
func RunWithFactory(ctx context.Context, tickers, dates []string, c config.Config, asset string, book *portfolio.Portfolio, selected []string, runID string, factory Factory, now time.Time) (Result, error) {
	if runID == "" {
		runID = now.Format("20060102_150405")
	}
	safe, e := dataflows.SafeComponent(runID)
	if e != nil {
		return Result{}, e
	}
	c.ResultsDir = filepath.Join(c.ResultsDir, "backtest", safe)
	c.MemoryLogPath = filepath.Join(c.ResultsDir, "trading_memory.md")
	runner, e := factory(c, selected)
	if e != nil {
		return Result{}, e
	}
	result := Result{RunID: runID, LogPath: c.MemoryLogPath}
	log := memory.Log{Path: c.MemoryLogPath}
	entries, e := log.Entries()
	if e != nil {
		return result, e
	}
	done := map[string]bool{}
	for _, entry := range entries {
		done[entry.Ticker+"|"+entry.Date] = true
	}
	for _, ticker := range tickers {
		for _, date := range dates {
			if e = ctx.Err(); e != nil {
				return result, e
			}
			if done[ticker+"|"+date] {
				result.Skipped++
				continue
			}
			if _, _, e = runner.Propagate(ctx, ticker, date, asset, book); e != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				result.Failures = append(result.Failures, Failure{ticker, date, e.Error()})
			} else {
				result.CellsRun++
			}
		}
	}
	for _, ticker := range tickers {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		if e = runner.SettlePending(ctx, ticker); e != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.SettlementFailures = append(result.SettlementFailures, Failure{Ticker: ticker, Reason: e.Error()})
		}
	}
	return result, nil
}

type RatingScore struct {
	Rating    string
	Count     int
	HitRate   *float64
	MeanAlpha float64
}
type Summary struct {
	Resolved, Pending, Unscored int
	ByRating                    []RatingScore
	Holding                     string
}

func Summarize(entries []memory.Entry) Summary {
	s := Summary{}
	alphas := map[string][]float64{}
	order := []string{}
	windows := map[string]bool{}
	for _, e := range entries {
		if e.Rating == "REVIEW" {
			s.Unscored++
			continue
		}
		if e.Pending {
			continue
		}
		a, err := strconv.ParseFloat(strings.TrimRight(strings.TrimSpace(e.Alpha), "%"), 64)
		if err != nil {
			continue
		}
		s.Resolved++
		if _, ok := alphas[e.Rating]; !ok {
			order = append(order, e.Rating)
		}
		alphas[e.Rating] = append(alphas[e.Rating], a/100)
		if strings.HasSuffix(e.Holding, "d") {
			windows[strings.TrimSuffix(e.Holding, "d")+" trading days"] = true
		}
	}
	s.Pending = len(entries) - s.Resolved - s.Unscored
	for _, rating := range order {
		values := alphas[rating]
		direction := map[string]int{"Buy": 1, "Overweight": 1, "Hold": 0, "Underweight": -1, "Sell": -1}[rating]
		sum, hits := 0.0, 0
		for _, a := range values {
			sum += a
			if a*float64(direction) > 0 {
				hits++
			}
		}
		score := RatingScore{Rating: rating, Count: len(values), MeanAlpha: sum / float64(len(values))}
		if direction != 0 {
			rate := float64(hits) / float64(len(values))
			score.HitRate = &rate
		}
		s.ByRating = append(s.ByRating, score)
	}
	parts := []string{}
	for w := range windows {
		parts = append(parts, w)
	}
	sort.Strings(parts)
	s.Holding = strings.Join(parts, ", ")
	if s.Holding == "" {
		s.Holding = "the configured window"
	}
	return s
}
func (s Summary) Render() string {
	first := fmt.Sprintf("Resolved cells: %d · pending: %d", s.Resolved, s.Pending)
	if s.Unscored > 0 {
		first += fmt.Sprintf(" · unscored: %d", s.Unscored)
	}
	lines := []string{first}
	for _, score := range s.ByRating {
		called := "no direction claimed"
		if score.HitRate != nil {
			called = fmt.Sprintf("called the direction %.0f%%", *score.HitRate*100)
		}
		lines = append(lines, fmt.Sprintf("- %s: n=%d, %s, mean alpha %+.2f%% vs the benchmark", score.Rating, score.Count, called, score.MeanAlpha*100))
	}
	lines = append(lines, "")
	if s.Pending > 0 {
		lines = append(lines, "Pending cells are not scored above; re-run to settle them.")
	}
	lines = append(lines, fmt.Sprintf("Alpha is measured over %s after each analysis date. One model sampling per cell, and text feeds are not archived, so these figures are indicative rather than repeatable.", s.Holding))
	return strings.Join(lines, "\n")
}
