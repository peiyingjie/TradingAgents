package backtest

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"tradingagents/internal/config"
	"tradingagents/internal/memory"
	"tradingagents/internal/portfolio"
	"tradingagents/internal/state"
)

type fakeRunner struct{ calls, settled []string }

func (r *fakeRunner) Propagate(ctx context.Context, ticker, date, asset string, p *portfolio.Portfolio) (state.State, string, error) {
	r.calls = append(r.calls, ticker+"/"+date)
	if ticker == "BAD" {
		return state.State{}, "", errors.New("source failed")
	}
	return state.State{}, "Hold", nil
}
func (r *fakeRunner) SettlePending(ctx context.Context, ticker string) error {
	r.settled = append(r.settled, ticker)
	return nil
}
func TestBacktestIsolatesRunSkipsRecordedCellsAndContinuesFailures(t *testing.T) {
	c := config.Config{ResultsDir: t.TempDir(), MemoryLogPath: "live-memory.md"}
	r := &fakeRunner{}
	factory := func(actual config.Config, selected []string) (Runner, error) {
		want := filepath.Join(c.ResultsDir, "backtest", "example")
		if actual.ResultsDir != want || actual.MemoryLogPath != filepath.Join(want, "trading_memory.md") {
			t.Fatalf("bad isolation %+v", actual)
		}
		log := memory.Log{Path: actual.MemoryLogPath}
		if e := log.Store("AAPL", "2026-08-01", "**Rating**: Buy"); e != nil {
			return nil, e
		}
		return r, nil
	}
	result, e := RunWithFactory(context.Background(), []string{"AAPL", "BAD"}, []string{"2026-08-01", "2026-08-08"}, c, "stock", nil, []string{"market"}, "example", factory, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if result.Skipped != 1 || result.CellsRun != 1 || len(result.Failures) != 2 {
		t.Fatalf("%+v", result)
	}
	if !reflect.DeepEqual(r.calls, []string{"AAPL/2026-08-08", "BAD/2026-08-01", "BAD/2026-08-08"}) || !reflect.DeepEqual(r.settled, []string{"AAPL", "BAD"}) {
		t.Fatal(r)
	}
	if c.MemoryLogPath != "live-memory.md" {
		t.Fatal("mutated caller config")
	}
}
func TestGridAndCancellation(t *testing.T) {
	now := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	dates, e := Grid("2026-08-01", "2026-12-01", 7, now)
	if e != nil || !reflect.DeepEqual(dates, []string{"2026-08-01", "2026-08-08", "2026-08-15"}) {
		t.Fatal(dates, e)
	}
	if _, e = Grid("2026-08-01", "2026-08-15", 0, now); e == nil {
		t.Fatal("accepted zero interval")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &fakeRunner{}
	_, e = RunWithFactory(ctx, []string{"AAPL"}, dates, config.Config{ResultsDir: t.TempDir()}, "stock", nil, nil, "cancelled", func(config.Config, []string) (Runner, error) { return r, nil }, now)
	if !errors.Is(e, context.Canceled) || len(r.calls) > 0 {
		t.Fatal(e, r)
	}
}
