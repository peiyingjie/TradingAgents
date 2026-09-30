package graph

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"tradingagents/internal/config"
	"tradingagents/internal/dataflows"
	"tradingagents/internal/memory"
	"tradingagents/internal/runtime"
	"tradingagents/internal/tools"
)

type offlineData struct {
	graphSources
	registry *tools.Registry
	bars     map[string][]dataflows.Bar
}

func (d offlineData) Registry() (*tools.Registry, error) { return d.registry, nil }
func (d offlineData) Info(context.Context, string) (map[string]any, error) {
	return map[string]any{"longName": "Apple Inc.", "exchange": "NMS"}, nil
}
func (d offlineData) Bars(ctx context.Context, ticker, start, end string) ([]dataflows.Bar, error) {
	return d.bars[ticker], nil
}
func testGraph(t *testing.T, checkpoint bool) (*TradingGraph, *graphRecorder) {
	t.Helper()
	c, e := config.Default()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	c.ResultsDir = filepath.Join(dir, "results")
	c.MemoryLogPath = filepath.Join(dir, "memory.md")
	c.DataCacheDir = filepath.Join(dir, "cache")
	c.CheckpointEnabled = checkpoint
	c.MaxRecurLimit = 100
	c.MaxDebateRounds = 1
	c.MaxRiskDiscussRounds = 1
	c.OutputLanguage = "English"
	s, r := fixtureService(t, c)
	b, e := os.ReadFile("testdata/python_graph.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct{ Samples map[string]json.RawMessage }
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	rec := &graphRecorder{samples: fixture.Samples, turns: map[string]int{}}
	g := NewWithDependencies(c, []string{"market", "social", "news", "fundamentals"}, rec, rec, offlineData{graphSources{s}, r, nil})
	g.Now = func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }
	return g, rec
}
func TestTradingGraphInterruptedRunResumesAndPersists(t *testing.T) {
	ctx := context.Background()
	g, rec := testGraph(t, true)
	stop := errors.New("simulated process interruption after checkpoint")
	g.OnEvent = func(e runtime.Event) error {
		if e.Node == "Research Manager" {
			return stop
		}
		return nil
	}
	if _, _, e := g.Propagate(ctx, "AAPL", "2026-08-14", "stock", nil); !errors.Is(e, stop) {
		t.Fatalf("got %v", e)
	}
	firstCalls := len(rec.calls)
	sig, e := g.Signature("stock", nil)
	if e != nil {
		t.Fatal(e)
	}
	thread := ThreadID("AAPL", "2026-08-14", sig)
	path := filepath.Join(g.Config.DataCacheDir, "checkpoints", "AAPL.db")
	store, e := runtime.OpenSQLite(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := store.Load(ctx, thread)
	if e != nil {
		t.Fatal(e)
	}
	if saved == nil || saved.NextNode != "Trader" {
		t.Fatalf("checkpoint %+v", saved)
	}
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	var resumed []string
	g.OnEvent = func(e runtime.Event) error {
		if e.Node != "" {
			resumed = append(resumed, e.Node)
		}
		return nil
	}
	final, signal, e := g.Propagate(ctx, "AAPL", "2026-08-14", "stock", nil)
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"Trader", "Aggressive Analyst", "Conservative Analyst", "Neutral Analyst", "Portfolio Manager"}
	if !reflect.DeepEqual(resumed, want) || len(rec.calls) != firstCalls+5 {
		t.Fatalf("replayed completed work: %v, calls %d", resumed, len(rec.calls)-firstCalls)
	}
	if signal != "Overweight" || final.FinalTradeDecision == "" {
		t.Fatal(signal, final.FinalTradeDecision)
	}
	entries, e := g.Memory.Entries()
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 1 || entries[0].Decision != final.FinalTradeDecision {
		t.Fatal(entries)
	}
	report, e := os.ReadFile(filepath.Join(g.Config.ResultsDir, "AAPL", "TradingAgentsStrategy_logs", "full_states_log_2026-08-14.json"))
	if e != nil {
		t.Fatal(e)
	}
	if !json.Valid(report) {
		t.Fatal("invalid state log")
	}
	store, e = runtime.OpenSQLite(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	saved, e = store.Load(ctx, thread)
	if e != nil || saved != nil {
		t.Fatal("completed checkpoint not cleared", e)
	}
}
func TestSettlementRequiresWholeWindowAndRecordsResolution(t *testing.T) {
	g, _ := testGraph(t, false)
	g.Config.HoldingPeriodDays = 2
	d := g.Data.(offlineData)
	makeBars := func(values ...float64) []dataflows.Bar {
		out := []dataflows.Bar{}
		for i, v := range values {
			v := v
			out = append(out, dataflows.Bar{Date: time.Date(2026, 8, 10+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), Close: &v})
		}
		return out
	}
	d.bars = map[string][]dataflows.Bar{"AAPL": makeBars(100, 101), "SPY": makeBars(100, 102, 104)}
	g.Data = d
	if e := g.Memory.Store("AAPL", "2026-08-10", "**Rating**: Buy"); e != nil {
		t.Fatal(e)
	}
	if e := g.SettlePending(context.Background(), "AAPL"); e != nil {
		t.Fatal(e)
	}
	entries, e := g.Memory.Entries()
	if e != nil {
		t.Fatal(e)
	}
	if !entries[0].Pending {
		t.Fatal("settled before complete holding window")
	}
	d.bars["AAPL"] = makeBars(100, 103, 110)
	if e = g.SettlePending(context.Background(), "AAPL"); e != nil {
		t.Fatal(e)
	}
	entries, e = g.Memory.Entries()
	if e != nil {
		t.Fatal(e)
	}
	if entries[0].Pending || entries[0].Alpha != "+6.0%" {
		t.Fatalf("outcome %+v", entries[0])
	}
	earlier, e := g.Memory.PastContext("AAPL", "2026-08-11")
	if e != nil {
		t.Fatal(e)
	}
	empty := memory.Log{Path: filepath.Join(t.TempDir(), "empty.md")}
	baseline, e := empty.PastContext("AAPL", "2026-08-11")
	if e != nil {
		t.Fatal(e)
	}
	if earlier != baseline {
		t.Fatal("future outcome leaked into historical context")
	}
}
