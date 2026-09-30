package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"tradingagents/internal/agents"
	"tradingagents/internal/config"
	"tradingagents/internal/dataflows"
	"tradingagents/internal/llm"
	"tradingagents/internal/runtime"
	"tradingagents/internal/state"
	"tradingagents/internal/tools"
	"tradingagents/pkg/model"
)

type recordedCall struct {
	Messages []model.Message
	Tools    []string
	Schema   string
}
type graphRecorder struct {
	samples map[string]json.RawMessage
	calls   []recordedCall
	turns   map[string]int
}

func (r *graphRecorder) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c := recordedCall{Messages: []model.Message{}, Tools: []string{}}
	for _, m := range req.Messages {
		c.Messages = append(c.Messages, model.Message{Type: m.Type, Content: m.Content})
	}
	if req.ToolChoice != "" {
		c.Schema = req.ToolChoice
	} else {
		for _, spec := range req.Tools {
			c.Tools = append(c.Tools, spec.Function.Name)
		}
	}
	r.calls = append(r.calls, c)
	if c.Schema != "" {
		var args map[string]any
		if err := json.Unmarshal(r.samples[c.Schema], &args); err != nil {
			return nil, err
		}
		m := model.NewMessage("ai", "")
		m.ToolCalls = []model.ToolCall{{ID: "structured", Name: c.Schema, Args: args}}
		return &llm.ChatResponse{Message: m}, nil
	}
	key := strings.Join(c.Tools, ",")
	turn := r.turns[key]
	r.turns[key]++
	if turn < len(req.Tools) {
		spec := req.Tools[turn]
		var schema struct{ Required []string }
		if err := json.Unmarshal(spec.Function.Parameters, &schema); err != nil {
			return nil, err
		}
		values := map[string]any{"symbol": "AAPL", "ticker": "AAPL", "indicator": "rsi", "start_date": "2026-08-01", "end_date": "2026-09-15", "curr_date": "2026-09-15", "topic": "Fed rate cut"}
		args := map[string]any{}
		for _, k := range schema.Required {
			args[k] = values[k]
		}
		m := model.NewMessage("ai", "")
		m.ToolCalls = []model.ToolCall{{ID: fmt.Sprintf("call-%d", len(r.calls)), Name: spec.Function.Name, Args: args}}
		return &llm.ChatResponse{Message: m}, nil
	}
	return &llm.ChatResponse{Message: model.NewMessage("ai", fmt.Sprintf("**Rating**: Buy\nEvidence %d", len(r.calls)))}, nil
}

type graphSources struct{ *dataflows.Service }

func (graphSources) StockTwits(context.Context, string, string, string) (string, error) {
	return "Fixed social evidence", nil
}
func (graphSources) Reddit(context.Context, string, string, string) (string, error) {
	return "Fixed social evidence", nil
}

func fixtureService(t *testing.T, c config.Config) (*dataflows.Service, *tools.Registry) {
	t.Helper()
	s, err := dataflows.New(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, vendors := range s.Vendors {
		for name := range vendors {
			vendors[name] = func(ctx context.Context, r dataflows.Request) (string, error) {
				if r.EndDate > "2026-08-14" || r.CurrentDate > "2026-08-14" {
					t.Errorf("tool received future date: %+v", r)
				}
				return "Fixed source evidence", nil
			}
		}
	}
	registry, err := s.Registry()
	if err != nil {
		t.Fatal(err)
	}
	original := registry.ByName["get_verified_market_snapshot"]
	registry.ByName["get_verified_market_snapshot"] = tools.Function{Definition: original.Spec(), Inject: original.Injected(), Run: func(context.Context, map[string]any) (model.Content, error) {
		return model.Text("Fixed source evidence"), nil
	}}
	return s, registry
}
func TestPythonCompleteGraphParity(t *testing.T) {
	b, err := os.ReadFile("testdata/python_graph.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Samples   map[string]json.RawMessage
		Scenarios []struct {
			Selected []string
			Rounds   int
			Nodes    []string
			Calls    []recordedCall
			Final    state.State
		}
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range fixture.Scenarios {
		t.Run(fmt.Sprintf("%s/rounds%d", strings.Join(scenario.Selected, "+"), scenario.Rounds), func(t *testing.T) {
			c, err := config.Default()
			if err != nil {
				t.Fatal(err)
			}
			c.OutputLanguage = "English"
			c.MaxDebateRounds = scenario.Rounds
			c.MaxRiskDiscussRounds = scenario.Rounds
			rec := &graphRecorder{samples: fixture.Samples, turns: map[string]int{}}
			service, registry := fixtureService(t, c)
			f, err := agents.New(rec, rec, c, graphSources{service})
			if err != nil {
				t.Fatal(err)
			}
			executor, err := Setup(f, scenario.Selected, registry, nil)
			if err != nil {
				t.Fatal(err)
			}
			initial := state.Initial("AAPL", "2026-08-14", "stock")
			initial.InstrumentContext = "AAPL / Apple / USD"
			input, err := state.AsUpdate(initial)
			if err != nil {
				t.Fatal(err)
			}
			nodes := []string{}
			final, err := executor.Invoke(context.Background(), &input, runtime.Options{RecursionLimit: 100, StreamMode: "updates", OnEvent: func(e runtime.Event) error { nodes = append(nodes, e.Node); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(nodes, scenario.Nodes) {
				t.Fatalf("nodes got %v want %v", nodes, scenario.Nodes)
			}
			if len(rec.calls) != len(scenario.Calls) {
				t.Fatalf("calls got %d want %d", len(rec.calls), len(scenario.Calls))
			}
			for i, got := range rec.calls {
				if !reflect.DeepEqual(got, scenario.Calls[i]) {
					a, _ := json.Marshal(got)
					b, _ := json.Marshal(scenario.Calls[i])
					t.Fatalf("model call %d differs\ngot %s\nwant %s", i, a, b)
				}
			}
			for i := range final.Messages {
				final.Messages[i].ID = ""
				if len(final.Messages[i].ToolCalls) == 0 {
					final.Messages[i].ToolCalls = nil
				}
			}
			for i := range scenario.Final.Messages {
				scenario.Final.Messages[i].ID = ""
				if len(scenario.Final.Messages[i].ToolCalls) == 0 {
					scenario.Final.Messages[i].ToolCalls = nil
				}
			}
			if !reflect.DeepEqual(final, scenario.Final) {
				a, _ := json.Marshal(final)
				b, _ := json.Marshal(scenario.Final)
				t.Fatalf("final state differs\ngot %s\nwant %s", a, b)
			}
		})
	}
}
