package agents

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"tradingagents/internal/config"
	"tradingagents/internal/llm"
	"tradingagents/internal/runtime"
	"tradingagents/internal/state"
	"tradingagents/pkg/model"
)

type recorder struct{ Messages []model.Message }

func (r *recorder) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	r.Messages = req.Messages
	return &llm.ChatResponse{Message: model.NewMessage("ai", "reference response")}, nil
}

type fixedSources struct{}

func (fixedSources) News(context.Context, string, string, string) (string, error) {
	return "news block", nil
}
func (fixedSources) StockTwits(context.Context, string, string, string) (string, error) {
	return "stocktwits block", nil
}
func (fixedSources) Reddit(context.Context, string, string, string) (string, error) {
	return "reddit block", nil
}
func TestPythonAgentParity(t *testing.T) {
	b, e := os.ReadFile("testdata/python_agents.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Agent, Language string
		State           state.State
		Messages        []model.Message
		Update          state.Update
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Agent+"/"+c.Language+"/"+c.State.MarketReport, func(t *testing.T) {
			rec := &recorder{}
			f, e := New(rec, rec, config.Config{OutputLanguage: c.Language}, fixedSources{})
			if e != nil {
				t.Fatal(e)
			}
			var node runtime.Node
			switch c.Agent {
			case "market", "news", "social", "fundamentals":
				node = f.Analyst(c.Agent)
			case "bull":
				node = f.Researcher(true)
			case "bear":
				node = f.Researcher(false)
			case "research":
				node = f.ResearchManager()
			case "trader":
				node = f.Trader()
			case "aggressive", "conservative", "neutral":
				node = f.RiskDebator(c.Agent)
			case "portfolio":
				node = f.PortfolioManager()
			}
			out, e := node(context.Background(), c.State)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(rec.Messages, c.Messages) {
				got, _ := json.MarshalIndent(rec.Messages, "", "  ")
				want, _ := json.MarshalIndent(c.Messages, "", "  ")
				t.Fatalf("prompt/message mismatch\ngot %s\nwant %s", got, want)
			}
			if !reflect.DeepEqual(out, c.Update) {
				got, _ := json.Marshal(out)
				want, _ := json.Marshal(c.Update)
				t.Fatalf("update mismatch\ngot %s\nwant %s", got, want)
			}
		})
	}
}
