package graph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"tradingagents/internal/agents"
	"tradingagents/internal/config"
	"tradingagents/internal/dataflows"
	"tradingagents/internal/llm"
	"tradingagents/internal/memory"
	"tradingagents/internal/portfolio"
	"tradingagents/internal/reporting"
	"tradingagents/internal/runtime"
	"tradingagents/internal/state"
	"tradingagents/internal/tools"
	"tradingagents/pkg/model"
)

type DataSource interface {
	agents.SentimentSource
	Registry() (*tools.Registry, error)
	Bars(context.Context, string, string, string) ([]dataflows.Bar, error)
	Info(context.Context, string) (map[string]any, error)
	Normalize(string) string
}
type TradingGraph struct {
	Config      config.Config
	Selected    []string
	Quick, Deep llm.Client
	Data        DataSource
	Memory      *memory.Log
	Now         func() time.Time
	OnEvent     func(runtime.Event) error
}

func New(c config.Config, selected []string) (*TradingGraph, error) {
	if _, e := Plan(selected); e != nil {
		return nil, e
	}
	quick, e := llm.New(c, c.QuickThinkLLM)
	if e != nil {
		return nil, e
	}
	deep, e := llm.New(c, c.DeepThinkLLM)
	if e != nil {
		return nil, e
	}
	data, e := dataflows.New(c)
	if e != nil {
		return nil, e
	}
	return NewWithDependencies(c, selected, quick, deep, data), nil
}
func NewWithDependencies(c config.Config, selected []string, quick, deep llm.Client, data DataSource) *TradingGraph {
	return &TradingGraph{Config: c, Selected: append([]string(nil), selected...), Quick: quick, Deep: deep, Data: data, Memory: &memory.Log{Path: c.MemoryLogPath, MaxEntries: c.MemoryLogMaxEntries}, Now: time.Now}
}
func ThreadID(ticker, date, signature string) string {
	base := strings.ToUpper(ticker) + ":" + date
	if signature != "" {
		base += ":" + signature
	}
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:])[:16]
}
func (g *TradingGraph) Signature(asset string, book *portfolio.Portfolio) (string, error) {
	fingerprint := "none"
	var e error
	if book != nil {
		fingerprint, e = book.Fingerprint()
		if e != nil {
			return "", e
		}
	}
	return fmt.Sprintf("analysts=%s|debate=%d|risk=%d|asset=%s|portfolio=%s", strings.Join(g.Selected, ","), g.Config.MaxDebateRounds, g.Config.MaxRiskDiscussRounds, asset, fingerprint), nil
}
func (g *TradingGraph) Benchmark(ticker string) string {
	if g.Config.BenchmarkTicker != "" {
		return g.Data.Normalize(g.Config.BenchmarkTicker)
	}
	ticker = g.Data.Normalize(ticker)
	for suffix, b := range g.Config.BenchmarkMap {
		if suffix != "" && strings.HasSuffix(ticker, strings.ToUpper(suffix)) {
			return b
		}
	}
	if b, ok := g.Config.BenchmarkMap[""]; ok {
		return b
	}
	return "SPY"
}
func (g *TradingGraph) SettlePending(ctx context.Context, ticker string) error {
	entries, e := g.Memory.Entries()
	if e != nil {
		return e
	}
	benchmark := g.Benchmark(ticker)
	updates := []memory.Outcome{}
	days := g.Config.HoldingPeriodDays
	for _, entry := range entries {
		if entry.Ticker != ticker || !entry.Pending {
			continue
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		start, e := dataflows.ParseDate(entry.Date)
		if e != nil {
			slog.Warn("cannot resolve decision date", "error", e)
			continue
		}
		end := start.AddDate(0, 0, int(math.RoundToEven(float64(days)*7/5))+6).Format("2006-01-02")
		stock, e := g.Data.Bars(ctx, ticker, entry.Date, end)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("cannot resolve outcome; will retry next run", "ticker", ticker, "error", e)
			continue
		}
		bench, e := g.Data.Bars(ctx, benchmark, entry.Date, end)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("cannot resolve benchmark; will retry next run", "error", e)
			continue
		}
		if days < 0 || len(stock) <= days || len(bench) <= days {
			continue
		}
		if stock[0].Close == nil || stock[days].Close == nil || bench[0].Close == nil || bench[days].Close == nil || *stock[0].Close == 0 || *bench[0].Close == 0 {
			continue
		}
		raw := (*stock[days].Close - *stock[0].Close) / *stock[0].Close
		alpha := raw - (*bench[days].Close-*bench[0].Close) / *bench[0].Close
		reflection, e := g.reflect(ctx, entry.Decision, raw, alpha, benchmark, days)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("reflection failed; will retry next run", "error", e)
			continue
		}
		updates = append(updates, memory.Outcome{Ticker: ticker, Date: entry.Date, RawReturn: raw, AlphaReturn: alpha, HoldingDays: days, Reflection: reflection, ResolutionDate: stock[days].Date})
	}
	return g.Memory.Update(updates)
}
func (g *TradingGraph) reflect(ctx context.Context, decision string, raw, alpha float64, benchmark string, days int) (string, error) {
	system := fmt.Sprintf("You are a trading analyst reviewing your own past decision now that the outcome is known.\nThe outcome covers %d trading days after the analysis date, which may be shorter than the horizon the decision was written for.\nWrite exactly 2-4 sentences of plain prose (no bullets, no headers, no markdown).\n\nCover in order:\n1. What the %d-day alpha shows about the directional call (cite the figure), and say so plainly if the window is too short to judge the thesis.\n2. Which part of the investment thesis this window supports or undercuts.\n3. One concrete lesson to apply to the next similar analysis.\n\nBe specific and terse. Your output will be stored verbatim in a decision log and re-read by future analysts, so every word must earn its place.", days, days)
	user := fmt.Sprintf("Raw return over %d trading days: %+.1f%%\nAlpha vs %s: %+.1f%%\n\nFinal Decision:\n%s", days, raw*100, benchmark, alpha*100, decision)
	r, e := g.Quick.Chat(ctx, llm.ChatRequest{Messages: []model.Message{model.NewMessage("system", system), model.NewMessage("human", user)}})
	if e != nil {
		return "", e
	}
	return r.Message.Content.String(), nil
}
func (g *TradingGraph) InitialState(ctx context.Context, ticker, date, asset string, book *portfolio.Portfolio) (state.State, error) {
	s := state.Initial(ticker, date, asset)
	if e := g.SettlePending(ctx, ticker); e != nil {
		return s, e
	}
	asOf := ""
	if date < g.Now().Format("2006-01-02") {
		asOf = date
	}
	past, e := g.Memory.PastContext(ticker, asOf)
	if e != nil {
		return s, e
	}
	s.PastContext = past
	if book != nil {
		s.PortfolioContext = book.Render(ticker)
	}
	s.InstrumentContext = agents.InstrumentContext(s)
	info, e := g.Data.Info(ctx, ticker)
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	if e != nil {
		slog.Debug("could not resolve instrument identity", "ticker", ticker, "error", e)
		return s, nil
	}
	clean := func(k string) string {
		v, _ := info[k].(string)
		v = strings.TrimSpace(v)
		switch strings.ToLower(v) {
		case "none", "n/a", "nan", "null":
			return ""
		}
		return v
	}
	name := clean("longName")
	if name == "" {
		name = clean("shortName")
	}
	details := []string{}
	if name != "" {
		label := "Company"
		if asset == "crypto" {
			label = "Name"
		}
		details = append(details, label+": "+name)
	}
	sector, industry := clean("sector"), clean("industry")
	if sector != "" && industry != "" {
		details = append(details, "Business classification: "+sector+" / "+industry)
	} else if sector != "" {
		details = append(details, "Sector: "+sector)
	} else if industry != "" {
		details = append(details, "Industry: "+industry)
	}
	if exchange := clean("exchange"); exchange != "" {
		details = append(details, "Exchange: "+exchange)
	}
	if len(details) > 0 {
		cryptoSuffix := " Treat it as a crypto asset rather than a company, and do not assume company fundamentals are available."
		s.InstrumentContext = strings.TrimSuffix(s.InstrumentContext, cryptoSuffix)
		s.InstrumentContext += " Resolved identity: " + strings.Join(details, "; ") + ". Do not substitute a different company or ticker unless a tool result explicitly disproves this resolved identity."
		if date < g.Now().Format("2006-01-02") {
			s.InstrumentContext += fmt.Sprintf(" This identity is how the vendor describes the instrument today (%s), not necessarily on %s: a name or classification changed since then would read as the current one.", g.Now().Format("2006-01-02"), date)
		}
		if asset == "crypto" {
			s.InstrumentContext += cryptoSuffix
		}
	}
	return s, nil
}
func (g *TradingGraph) Propagate(ctx context.Context, ticker, date, asset string, book *portfolio.Portfolio) (final state.State, signal string, err error) {
	if _, err = dataflows.ParseDate(date); err != nil {
		return
	}
	if date > g.Now().Format("2006-01-02") {
		err = fmt.Errorf("trade date cannot be in the future: %s", date)
		return
	}
	safe, e := dataflows.SafeComponent(ticker)
	if e != nil {
		err = e
		return
	}
	if asset == "" {
		asset = "stock"
	}
	var store *runtime.SQLiteStore
	var checkpoint runtime.CheckpointStore
	thread := ""
	resume := false
	if g.Config.CheckpointEnabled {
		sig, e := g.Signature(asset, book)
		if e != nil {
			err = e
			return
		}
		thread = ThreadID(ticker, date, sig)
		dir := filepath.Join(g.Config.DataCacheDir, "checkpoints")
		if err = os.MkdirAll(dir, 0755); err != nil {
			return
		}
		store, err = runtime.OpenSQLite(ctx, filepath.Join(dir, strings.ToUpper(safe)+".db"))
		if err != nil {
			return
		}
		defer func() { err = errors.Join(err, store.Close()) }()
		checkpoint = store
		saved, e := store.Load(ctx, thread)
		if e != nil {
			err = e
			return
		}
		resume = saved != nil
	}
	f, e := agents.New(g.Quick, g.Deep, g.Config, g.Data)
	if e != nil {
		err = e
		return
	}
	registry, e := g.Data.Registry()
	if e != nil {
		err = e
		return
	}
	executor, e := Setup(f, g.Selected, registry, checkpoint)
	if e != nil {
		err = e
		return
	}
	initial, e := g.InitialState(ctx, ticker, date, asset, book)
	if e != nil {
		err = e
		return
	}
	u, e := state.AsUpdate(initial)
	if e != nil {
		err = e
		return
	}
	input := &u
	if resume {
		input = nil
	}
	final, err = executor.Invoke(ctx, input, runtime.Options{RecursionLimit: g.Config.MaxRecurLimit, ThreadID: thread, OnEvent: g.OnEvent})
	if err != nil {
		return
	}
	b, e := reporting.StateLog(final)
	if e != nil {
		err = e
		return
	}
	path := filepath.Join(g.Config.ResultsDir, safe, "TradingAgentsStrategy_logs", "full_states_log_"+date+".json")
	if err = dataflows.AtomicWrite(path, b); err != nil {
		return
	}
	if final.FinalTradeDecision != "" {
		if err = g.Memory.Store(ticker, date, final.FinalTradeDecision); err != nil {
			return
		}
	}
	if store != nil {
		if err = store.Delete(ctx, thread); err != nil {
			return
		}
	}
	signal = memory.Rating(final.FinalTradeDecision)
	return
}
func ClearCheckpoints(cacheDir string) (int, error) {
	dir := filepath.Join(cacheDir, "checkpoints")
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		base := entry.Name()
		for _, side := range entries {
			if !side.IsDir() && (side.Name() == base || strings.HasPrefix(side.Name(), base+"-")) {
				if e = os.Remove(filepath.Join(dir, side.Name())); e != nil && !os.IsNotExist(e) {
					return count, e
				}
			}
		}
		count++
	}
	return count, nil
}
