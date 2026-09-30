package graph

import (
	"context"
	"fmt"
	"strings"
	"tradingagents/internal/agents"
	"tradingagents/internal/runtime"
	"tradingagents/internal/state"
	"tradingagents/internal/tools"
)

type AnalystSpec struct{ Key, AgentNode, ClearNode, ToolNode, ReportKey string }

var AnalystSpecs = map[string]AnalystSpec{"market": {"market", "Market Analyst", "Msg Clear Market", "tools_market", "market_report"}, "social": {"social", "Sentiment Analyst", "Msg Clear Sentiment", "tools_social", "sentiment_report"}, "news": {"news", "News Analyst", "Msg Clear News", "tools_news", "news_report"}, "fundamentals": {"fundamentals", "Fundamentals Analyst", "Msg Clear Fundamentals", "tools_fundamentals", "fundamentals_report"}}

func Plan(selected []string) ([]AnalystSpec, error) {
	if len(selected) == 0 {
		return nil, fmt.Errorf("at least one analyst must be selected")
	}
	out := []AnalystSpec{}
	for _, key := range selected {
		spec, ok := AnalystSpecs[key]
		if !ok {
			return nil, fmt.Errorf("unknown analyst key: %s", key)
		}
		out = append(out, spec)
	}
	return out, nil
}
func DebateRoute(rounds int) runtime.Router {
	return func(ctx context.Context, s state.State) (string, error) {
		if s.InvestmentDebate.Count >= 2*rounds {
			return "Research Manager", nil
		}
		if strings.HasPrefix(s.InvestmentDebate.CurrentResponse, "Bull") {
			return "Bear Researcher", nil
		}
		return "Bull Researcher", nil
	}
}
func RiskRoute(rounds int) runtime.Router {
	return func(ctx context.Context, s state.State) (string, error) {
		if s.RiskDebate.Count >= 3*rounds {
			return "Portfolio Manager", nil
		}
		if strings.HasPrefix(s.RiskDebate.LatestSpeaker, "Aggressive") {
			return "Conservative Analyst", nil
		}
		if strings.HasPrefix(s.RiskDebate.LatestSpeaker, "Conservative") {
			return "Neutral Analyst", nil
		}
		return "Aggressive Analyst", nil
	}
}
func Setup(f *agents.Factory, selected []string, registry *tools.Registry, store runtime.CheckpointStore) (*runtime.Executor, error) {
	plan, err := Plan(selected)
	if err != nil {
		return nil, err
	}
	g := runtime.NewGraph()
	for _, spec := range plan {
		if err = g.AddNode(spec.AgentNode, f.Analyst(spec.Key)); err != nil {
			return nil, err
		}
		if err = g.AddNode(spec.ClearNode, agents.ClearMessages); err != nil {
			return nil, err
		}
		items := []tools.Tool{}
		names := append([]string(nil), agents.AnalystTools[spec.Key]...)
		if spec.Key == "news" {
			names = append(names, "get_insider_transactions")
		}
		for _, n := range names {
			t, ok := registry.ByName[n]
			if !ok {
				return nil, fmt.Errorf("missing tool %s", n)
			}
			items = append(items, t)
		}
		r, e := tools.NewRegistry(items)
		if e != nil {
			return nil, e
		}
		executor := &tools.Executor{Registry: r}
		if err = g.AddNode(spec.ToolNode, executor.Invoke); err != nil {
			return nil, err
		}
	}
	nodes := map[string]runtime.Node{"Bull Researcher": f.Researcher(true), "Bear Researcher": f.Researcher(false), "Research Manager": f.ResearchManager(), "Trader": f.Trader(), "Aggressive Analyst": f.RiskDebator("aggressive"), "Conservative Analyst": f.RiskDebator("conservative"), "Neutral Analyst": f.RiskDebator("neutral"), "Portfolio Manager": f.PortfolioManager()}
	for name, node := range nodes {
		if err = g.AddNode(name, node); err != nil {
			return nil, err
		}
	}
	if err = g.AddEdge(runtime.Start, plan[0].AgentNode); err != nil {
		return nil, err
	}
	for i, spec := range plan {
		router := func(ctx context.Context, s state.State) (string, error) {
			if len(s.Messages) == 0 {
				return "", fmt.Errorf("analyst returned no messages")
			}
			if len(s.Messages[len(s.Messages)-1].ToolCalls) > 0 {
				return spec.ToolNode, nil
			}
			return spec.ClearNode, nil
		}
		if err = g.AddRoute(spec.AgentNode, router, map[string]string{spec.ToolNode: spec.ToolNode, spec.ClearNode: spec.ClearNode}); err != nil {
			return nil, err
		}
		if err = g.AddEdge(spec.ToolNode, spec.AgentNode); err != nil {
			return nil, err
		}
		next := "Bull Researcher"
		if i+1 < len(plan) {
			next = plan[i+1].AgentNode
		}
		if err = g.AddEdge(spec.ClearNode, next); err != nil {
			return nil, err
		}
	}
	debate := map[string]string{"Bull Researcher": "Bull Researcher", "Bear Researcher": "Bear Researcher", "Research Manager": "Research Manager"}
	risk := map[string]string{"Aggressive Analyst": "Aggressive Analyst", "Conservative Analyst": "Conservative Analyst", "Neutral Analyst": "Neutral Analyst", "Portfolio Manager": "Portfolio Manager"}
	for _, n := range []string{"Bull Researcher", "Bear Researcher"} {
		if err = g.AddRoute(n, DebateRoute(f.Config.MaxDebateRounds), debate); err != nil {
			return nil, err
		}
	}
	for _, n := range []string{"Aggressive Analyst", "Conservative Analyst", "Neutral Analyst"} {
		if err = g.AddRoute(n, RiskRoute(f.Config.MaxRiskDiscussRounds), risk); err != nil {
			return nil, err
		}
	}
	for from, to := range map[string]string{"Research Manager": "Trader", "Trader": "Aggressive Analyst", "Portfolio Manager": runtime.End} {
		if err = g.AddEdge(from, to); err != nil {
			return nil, err
		}
	}
	return g.Compile(store)
}
