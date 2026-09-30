package memory_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"tradingagents/internal/backtest"
	"tradingagents/internal/memory"
)

func TestPythonMemoryAndSummaryParity(t *testing.T) {
	b, e := os.ReadFile("testdata/python_memory.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct{ Log, Live, Historical, Summary string }
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	log := memory.Log{Path: filepath.Join(t.TempDir(), "memory.md")}
	for _, v := range [][3]string{{"AAPL", "2025-01-01", "**Rating**: Buy\nThesis A"}, {"MSFT", "2025-01-02", "**Rating**: Sell\nThesis B"}, {"AAPL", "2025-01-03", "unclear"}, {"AAPL", "2025-01-04", "**Rating**: Hold"}} {
		if e = log.Store(v[0], v[1], v[2]); e != nil {
			t.Fatal(e)
		}
	}
	if e = log.Update([]memory.Outcome{{Ticker: "AAPL", Date: "2025-01-01", RawReturn: .052, AlphaReturn: .023, HoldingDays: 5, Reflection: "lesson A", ResolutionDate: "2025-01-08"}, {Ticker: "MSFT", Date: "2025-01-02", RawReturn: -.021, AlphaReturn: -.031, HoldingDays: 5, Reflection: "lesson B", ResolutionDate: "2025-01-09"}}); e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile(log.Path)
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != f.Log {
		t.Errorf("log mismatch\ngot %s\nwant %s", b, f.Log)
	}
	for cutoff, want := range map[string]string{"": f.Live, "2025-01-08": f.Historical} {
		got, e := log.PastContext("AAPL", cutoff)
		if e != nil || got != want {
			t.Errorf("context %s got %q want %q (%v)", cutoff, got, want, e)
		}
	}
	entries, e := log.Entries()
	if e != nil {
		t.Fatal(e)
	}
	if got := backtest.Summarize(entries).Render(); got != f.Summary {
		t.Errorf("summary got %q want %q", got, f.Summary)
	}
	if e = log.Store("AAPL", "2025-01-01", "duplicate"); e != nil {
		t.Fatal(e)
	}
	entries, e = log.Entries()
	if e != nil || len(entries) != 4 {
		t.Fatal(entries, e)
	}
}
