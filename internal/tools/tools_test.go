package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"tradingagents/internal/state"
	"tradingagents/pkg/model"
)

func TestOrderedParallelToolsAndInjection(t *testing.T) {
	spec := Spec{Type: "function", Function: FunctionSpec{Name: "echo", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`)}}
	r, e := NewRegistry([]Tool{Function{Definition: spec, Inject: map[string]string{"trade_date": "trade_date"}, Run: func(ctx context.Context, a map[string]any) (model.Content, error) {
		if a["trade_date"] != "2025-01-01" {
			t.Error("state injection lost")
		}
		if a["value"] == "first" {
			time.Sleep(5 * time.Millisecond)
		}
		return model.Text(a["value"].(string)), nil
	}}})
	if e != nil {
		t.Fatal(e)
	}
	exec := Executor{Registry: r}
	s := state.Initial("AAPL", "2025-01-01", "stock")
	m := model.NewMessage("ai", "")
	m.ToolCalls = []model.ToolCall{{ID: "1", Name: "echo", Args: map[string]any{"value": "first", "trade_date": "2099-01-01"}}, {ID: "2", Name: "echo", Args: map[string]any{"value": "second"}}, {ID: "3", Name: "echo", Args: map[string]any{}}}
	s.Messages = append(s.Messages, m)
	out, e := exec.Invoke(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	if out.Messages[0].Content.String() != "first" || out.Messages[1].ToolCallID != "2" || out.Messages[2].Status != "error" {
		t.Fatal(out)
	}
}

func TestNumericToolCoercionMatchesPython(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"days":{"type":"integer"}},"required":["days"]}`)
	for _, v := range []any{"30", "30.0", float64(30)} {
		args := map[string]any{"days": v}
		if e := Validate(schema, args); e != nil {
			t.Fatal(e)
		}
		if args["days"] != float64(30) {
			t.Fatal(args)
		}
	}
	for _, v := range []any{"30.1", "NaN", "wrong", nil} {
		if e := Validate(schema, map[string]any{"days": v}); e == nil {
			t.Fatalf("accepted invalid integer %v", v)
		}
	}
}
