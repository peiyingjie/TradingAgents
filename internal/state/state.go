package state

import (
	"encoding/json"
	"tradingagents/pkg/model"
)

type InvestmentDebate struct {
	BullHistory     string `json:"bull_history"`
	BearHistory     string `json:"bear_history"`
	History         string `json:"history"`
	CurrentResponse string `json:"current_response"`
	JudgeDecision   string `json:"judge_decision,omitempty"`
	Count           int    `json:"count"`
}
type RiskDebate struct {
	AggressiveHistory           string `json:"aggressive_history"`
	ConservativeHistory         string `json:"conservative_history"`
	NeutralHistory              string `json:"neutral_history"`
	History                     string `json:"history"`
	LatestSpeaker               string `json:"latest_speaker"`
	CurrentAggressiveResponse   string `json:"current_aggressive_response"`
	CurrentConservativeResponse string `json:"current_conservative_response"`
	CurrentNeutralResponse      string `json:"current_neutral_response"`
	JudgeDecision               string `json:"judge_decision,omitempty"`
	Count                       int    `json:"count"`
}
type State struct {
	Messages             []model.Message  `json:"messages"`
	CompanyOfInterest    string           `json:"company_of_interest"`
	AssetType            string           `json:"asset_type"`
	InstrumentContext    string           `json:"instrument_context"`
	TradeDate            string           `json:"trade_date"`
	Sender               string           `json:"sender,omitempty"`
	MarketReport         string           `json:"market_report"`
	SentimentReport      string           `json:"sentiment_report"`
	NewsReport           string           `json:"news_report"`
	FundamentalsReport   string           `json:"fundamentals_report"`
	InvestmentDebate     InvestmentDebate `json:"investment_debate_state"`
	InvestmentPlan       string           `json:"investment_plan,omitempty"`
	TraderInvestmentPlan string           `json:"trader_investment_plan,omitempty"`
	RiskDebate           RiskDebate       `json:"risk_debate_state"`
	FinalTradeDecision   string           `json:"final_trade_decision,omitempty"`
	PastContext          string           `json:"past_context"`
	PortfolioContext     string           `json:"portfolio_context"`
}

// Optional fields distinguish absent updates from explicit zero/empty values.
type Update struct {
	Messages             []model.Message   `json:"messages,omitempty"`
	CompanyOfInterest    *string           `json:"company_of_interest,omitempty"`
	AssetType            *string           `json:"asset_type,omitempty"`
	InstrumentContext    *string           `json:"instrument_context,omitempty"`
	TradeDate            *string           `json:"trade_date,omitempty"`
	Sender               *string           `json:"sender,omitempty"`
	MarketReport         *string           `json:"market_report,omitempty"`
	SentimentReport      *string           `json:"sentiment_report,omitempty"`
	NewsReport           *string           `json:"news_report,omitempty"`
	FundamentalsReport   *string           `json:"fundamentals_report,omitempty"`
	InvestmentDebate     *InvestmentDebate `json:"investment_debate_state,omitempty"`
	InvestmentPlan       *string           `json:"investment_plan,omitempty"`
	TraderInvestmentPlan *string           `json:"trader_investment_plan,omitempty"`
	RiskDebate           *RiskDebate       `json:"risk_debate_state,omitempty"`
	FinalTradeDecision   *string           `json:"final_trade_decision,omitempty"`
	PastContext          *string           `json:"past_context,omitempty"`
	PortfolioContext     *string           `json:"portfolio_context,omitempty"`
}

func Ptr[T any](v T) *T { return &v }
func Initial(ticker, date, asset string) State {
	return State{Messages: []model.Message{model.NewMessage("human", ticker)}, CompanyOfInterest: ticker, TradeDate: date, AssetType: asset}
}
func Clone(s State) (State, error) {
	b, e := json.Marshal(s)
	if e != nil {
		return State{}, e
	}
	var out State
	e = json.Unmarshal(b, &out)
	return out, e
}
func Merge(s State, u Update) (State, error) {
	// Decode into the typed state so objects replace, rather than recursively merge.
	if u.InvestmentDebate != nil {
		s.InvestmentDebate = *u.InvestmentDebate
	}
	if u.RiskDebate != nil {
		s.RiskDebate = *u.RiskDebate
	}
	messages := u.Messages
	u.Messages = nil
	u.InvestmentDebate = nil
	u.RiskDebate = nil
	b, e := json.Marshal(u)
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, e
	}
	if messages != nil {
		s.Messages, e = model.AddMessages(s.Messages, messages)
	}
	return s, e
}
func AsUpdate(s State) (Update, error) {
	b, e := json.Marshal(s)
	if e != nil {
		return Update{}, e
	}
	var u Update
	e = json.Unmarshal(b, &u)
	return u, e
}
