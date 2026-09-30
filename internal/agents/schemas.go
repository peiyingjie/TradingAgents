package agents

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type OptionalPrice struct{ Value *float64 }

func (p *OptionalPrice) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		p.Value = nil
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		s = strings.TrimSpace(s)
		if strings.HasSuffix(s, "%") {
			return nil
		}
		s = strings.TrimSpace(strings.TrimLeft(strings.ReplaceAll(s, ",", ""), "$€£¥"))
		n, e := strconv.ParseFloat(s, 64)
		if e != nil {
			return nil
		}
		p.Value = &n
		return nil
	}
	var n float64
	if e := json.Unmarshal(b, &n); e != nil {
		return e
	}
	p.Value = &n
	return nil
}
func (p OptionalPrice) String() string {
	if p.Value == nil {
		return "not provided"
	}
	v := strconv.FormatFloat(*p.Value, 'f', -1, 64)
	if !strings.Contains(v, ".") {
		v += ".0"
	}
	return v
}

type ResearchPlan struct {
	Recommendation   string `json:"recommendation"`
	Rationale        string `json:"rationale"`
	StrategicActions string `json:"strategic_actions"`
}
type TraderProposal struct {
	Action         string        `json:"action"`
	Reasoning      string        `json:"reasoning"`
	EntryPrice     OptionalPrice `json:"entry_price"`
	StopLoss       OptionalPrice `json:"stop_loss"`
	PositionSizing *string       `json:"position_sizing"`
}
type PortfolioDecision struct {
	Rating           string        `json:"rating"`
	ExecutiveSummary string        `json:"executive_summary"`
	InvestmentThesis string        `json:"investment_thesis"`
	PriceTarget      OptionalPrice `json:"price_target"`
	TimeHorizon      *string       `json:"time_horizon"`
}
type SentimentReport struct {
	OverallBand  string  `json:"overall_band"`
	OverallScore float64 `json:"overall_score"`
	Confidence   string  `json:"confidence"`
	Narrative    string  `json:"narrative"`
}

func validRating(s string) bool {
	return s == "Buy" || s == "Overweight" || s == "Hold" || s == "Underweight" || s == "Sell"
}
func optionalText(p *string) string {
	if p == nil || *p == "" {
		return "not provided"
	}
	return *p
}
func renderStructured(name string, data []byte) (string, error) {
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil {
		return "", e
	}
	required := map[string][]string{"ResearchPlan": {"recommendation", "rationale", "strategic_actions"}, "TraderProposal": {"action", "reasoning"}, "PortfolioDecision": {"rating", "executive_summary", "investment_thesis"}, "SentimentReport": {"overall_band", "overall_score", "confidence", "narrative"}}
	for _, f := range required[name] {
		if v, ok := fields[f]; !ok || string(v) == "null" {
			return "", fmt.Errorf("%s requires %s", name, f)
		}
	}
	switch name {
	case "ResearchPlan":
		var p ResearchPlan
		if e := json.Unmarshal(data, &p); e != nil {
			return "", e
		}
		if !validRating(p.Recommendation) {
			return "", fmt.Errorf("invalid recommendation: %q", p.Recommendation)
		}
		return fmt.Sprintf("**Recommendation**: %s\n\n**Rationale**: %s\n\n**Strategic Actions**: %s", p.Recommendation, p.Rationale, p.StrategicActions), nil
	case "TraderProposal":
		var p TraderProposal
		if e := json.Unmarshal(data, &p); e != nil {
			return "", e
		}
		if p.Action != "Buy" && p.Action != "Hold" && p.Action != "Sell" {
			return "", fmt.Errorf("invalid action: %q", p.Action)
		}
		return fmt.Sprintf("**Action**: %s\n\n**Reasoning**: %s\n\n**Entry Price**: %s\n\n**Stop Loss**: %s\n\n**Position Sizing**: %s\n\nFINAL TRANSACTION PROPOSAL: **%s**", p.Action, p.Reasoning, p.EntryPrice.String(), p.StopLoss.String(), optionalText(p.PositionSizing), strings.ToUpper(p.Action)), nil
	case "PortfolioDecision":
		var p PortfolioDecision
		if e := json.Unmarshal(data, &p); e != nil {
			return "", e
		}
		if !validRating(p.Rating) {
			return "", fmt.Errorf("invalid rating: %q", p.Rating)
		}
		return fmt.Sprintf("**Rating**: %s\n\n**Executive Summary**: %s\n\n**Investment Thesis**: %s\n\n**Price Target**: %s\n\n**Time Horizon**: %s", p.Rating, p.ExecutiveSummary, p.InvestmentThesis, p.PriceTarget.String(), optionalText(p.TimeHorizon)), nil
	case "SentimentReport":
		var p SentimentReport
		if e := json.Unmarshal(data, &p); e != nil {
			return "", e
		}
		valid := false
		for _, b := range []string{"Bullish", "Mildly Bullish", "Neutral", "Mixed", "Mildly Bearish", "Bearish"} {
			valid = valid || p.OverallBand == b
		}
		if !valid || p.OverallScore < 0 || p.OverallScore > 10 || (p.Confidence != "low" && p.Confidence != "medium" && p.Confidence != "high") {
			return "", fmt.Errorf("invalid sentiment report")
		}
		return fmt.Sprintf("**Overall Sentiment:** **%s** (Score: %.1f/10)\n**Confidence:** %s\n\n%s", p.OverallBand, p.OverallScore, strings.ToUpper(p.Confidence[:1])+p.Confidence[1:], p.Narrative), nil
	}
	return "", fmt.Errorf("unknown structured schema: %s", name)
}
