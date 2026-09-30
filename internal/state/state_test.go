package state

import (
	"testing"
	"tradingagents/pkg/model"
)

func TestMergeReplaceAndIsolation(t *testing.T) {
	s := Initial("AAPL", "2025-01-02", "stock")
	s.InvestmentDebate = InvestmentDebate{JudgeDecision: "old", Count: 2}
	s.MarketReport = "old"
	out, e := Merge(s, Update{InvestmentDebate: &InvestmentDebate{Count: 3}, MarketReport: Ptr(""), Messages: []model.Message{model.NewMessage("ai", "answer")}})
	if e != nil {
		t.Fatal(e)
	}
	if out.InvestmentDebate.JudgeDecision != "" || out.InvestmentDebate.Count != 3 || out.MarketReport != "" || len(out.Messages) != 2 {
		t.Fatal(out)
	}
	if s.MarketReport != "old" || len(s.Messages) != 1 {
		t.Fatal("input modified")
	}
}
