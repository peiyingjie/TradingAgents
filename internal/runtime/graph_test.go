package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"tradingagents/internal/state"
	"tradingagents/pkg/model"
)

func TestCheckpointResumeDoesNotRepeatCompletedNode(t *testing.T) {
	ctx := context.Background()
	store, e := OpenSQLite(ctx, filepath.Join(t.TempDir(), "checkpoint.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	g := NewGraph()
	calls := 0
	fail := true
	for name, node := range map[string]Node{"first": func(ctx context.Context, s state.State) (state.Update, error) {
		calls++
		s.Messages[0].Content = model.Text("mutated snapshot")
		return state.Update{MarketReport: state.Ptr("report")}, nil
	}, "second": func(ctx context.Context, s state.State) (state.Update, error) {
		if fail {
			return state.Update{}, errors.New("interrupted")
		}
		return state.Update{FinalTradeDecision: state.Ptr("done")}, nil
	}} {
		if e = g.AddNode(name, node); e != nil {
			t.Fatal(e)
		}
	}
	for a, b := range map[string]string{Start: "first", "first": "second", "second": End} {
		if e = g.AddEdge(a, b); e != nil {
			t.Fatal(e)
		}
	}
	exec, e := g.Compile(store)
	if e != nil {
		t.Fatal(e)
	}
	input, e := state.AsUpdate(state.Initial("AAPL", "2025-01-01", "stock"))
	if e != nil {
		t.Fatal(e)
	}
	opts := Options{ThreadID: "run"}
	if _, e = exec.Invoke(ctx, &input, opts); e == nil {
		t.Fatal("expected failure")
	}
	saved, e := store.Load(ctx, "run")
	if e != nil || saved.Step != 1 || saved.NextNode != "second" || saved.State.Messages[0].Content.String() != "AAPL" {
		t.Fatalf("%+v %v", saved, e)
	}
	fail = false
	out, e := exec.Invoke(ctx, nil, opts)
	if e != nil || calls != 1 || out.FinalTradeDecision != "done" {
		t.Fatalf("%+v %v calls=%d", out, e, calls)
	}
}
func TestRouterAndLimit(t *testing.T) {
	g := NewGraph()
	if e := g.AddNode("loop", func(ctx context.Context, s state.State) (state.Update, error) {
		d := s.InvestmentDebate
		d.Count++
		return state.Update{InvestmentDebate: &d}, nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := g.AddEdge(Start, "loop"); e != nil {
		t.Fatal(e)
	}
	if e := g.AddRoute("loop", func(ctx context.Context, s state.State) (string, error) {
		if s.InvestmentDebate.Count == 3 {
			return "end", nil
		}
		return "again", nil
	}, map[string]string{"end": End, "again": "loop"}); e != nil {
		t.Fatal(e)
	}
	exec, e := g.Compile(nil)
	if e != nil {
		t.Fatal(e)
	}
	input := state.Update{}
	out, e := exec.Invoke(context.Background(), &input, Options{RecursionLimit: 3})
	if e != nil || out.InvestmentDebate.Count != 3 {
		t.Fatal(out, e)
	}
	_, e = exec.Invoke(context.Background(), &input, Options{RecursionLimit: 2})
	var recursion *RecursionError
	if !errors.As(e, &recursion) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = exec.Invoke(ctx, &input, Options{})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
