package reporting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"tradingagents/internal/state"
)

func Write(s state.State, ticker, path string, now time.Time) (string, error) {
	groups := []struct {
		Dir, Title string
		Items      [][3]string
	}{
		{"1_analysts", "I. Analyst Team Reports", [][3]string{{"market", "Market Analyst", s.MarketReport}, {"sentiment", "Sentiment Analyst", s.SentimentReport}, {"news", "News Analyst", s.NewsReport}, {"fundamentals", "Fundamentals Analyst", s.FundamentalsReport}}},
		{"2_research", "II. Research Team Decision", [][3]string{{"bull", "Bull Researcher", s.InvestmentDebate.BullHistory}, {"bear", "Bear Researcher", s.InvestmentDebate.BearHistory}, {"manager", "Research Manager", s.InvestmentDebate.JudgeDecision}}},
		{"3_trading", "III. Trading Team Plan", [][3]string{{"trader", "Trader", s.TraderInvestmentPlan}}},
		{"4_risk", "IV. Risk Management Team Decision", [][3]string{{"aggressive", "Aggressive Analyst", s.RiskDebate.AggressiveHistory}, {"conservative", "Conservative Analyst", s.RiskDebate.ConservativeHistory}, {"neutral", "Neutral Analyst", s.RiskDebate.NeutralHistory}}},
		{"5_portfolio", "V. Portfolio Manager Decision", [][3]string{{"decision", "Portfolio Manager", s.RiskDebate.JudgeDecision}}},
	}
	if e := os.MkdirAll(path, 0755); e != nil {
		return "", e
	}
	sections := []string{}
	for _, g := range groups {
		parts := []string{}
		for _, item := range g.Items {
			if item[2] == "" {
				continue
			}
			dir := filepath.Join(path, g.Dir)
			if e := os.MkdirAll(dir, 0755); e != nil {
				return "", e
			}
			if e := os.WriteFile(filepath.Join(dir, item[0]+".md"), []byte(item[2]), 0644); e != nil {
				return "", e
			}
			parts = append(parts, "### "+item[1]+"\n"+item[2])
		}
		if len(parts) > 0 {
			sections = append(sections, "## "+g.Title+"\n\n"+strings.Join(parts, "\n\n"))
		}
	}
	header := fmt.Sprintf("# Trading Analysis Report: %s\n\nGenerated: %s\n\n", ticker, now.Format("2006-01-02 15:04:05"))
	result := filepath.Join(path, "complete_report.md")
	return result, os.WriteFile(result, []byte(header+strings.Join(sections, "\n\n")), 0644)
}
func StateLog(s state.State) ([]byte, error) {
	type research struct {
		BullHistory     string `json:"bull_history"`
		BearHistory     string `json:"bear_history"`
		History         string `json:"history"`
		CurrentResponse string `json:"current_response"`
		JudgeDecision   string `json:"judge_decision"`
	}
	type risk struct {
		AggressiveHistory   string `json:"aggressive_history"`
		ConservativeHistory string `json:"conservative_history"`
		NeutralHistory      string `json:"neutral_history"`
		History             string `json:"history"`
		JudgeDecision       string `json:"judge_decision"`
	}
	v := struct {
		Company      string   `json:"company_of_interest"`
		Date         string   `json:"trade_date"`
		Market       string   `json:"market_report"`
		Sentiment    string   `json:"sentiment_report"`
		News         string   `json:"news_report"`
		Fundamentals string   `json:"fundamentals_report"`
		Investment   research `json:"investment_debate_state"`
		Trader       string   `json:"trader_investment_decision"`
		Risk         risk     `json:"risk_debate_state"`
		Plan         string   `json:"investment_plan"`
		Final        string   `json:"final_trade_decision"`
	}{s.CompanyOfInterest, s.TradeDate, s.MarketReport, s.SentimentReport, s.NewsReport, s.FundamentalsReport, research{s.InvestmentDebate.BullHistory, s.InvestmentDebate.BearHistory, s.InvestmentDebate.History, s.InvestmentDebate.CurrentResponse, s.InvestmentDebate.JudgeDecision}, s.TraderInvestmentPlan, risk{s.RiskDebate.AggressiveHistory, s.RiskDebate.ConservativeHistory, s.RiskDebate.NeutralHistory, s.RiskDebate.History, s.RiskDebate.JudgeDecision}, s.InvestmentPlan, s.FinalTradeDecision}
	return json.MarshalIndent(v, "", "    ")
}
