package agents

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"tradingagents/internal/config"
	"tradingagents/internal/llm"
	"tradingagents/internal/runtime"
	"tradingagents/internal/state"
	"tradingagents/internal/tools"
	"tradingagents/pkg/model"
)

//go:embed prompts/*
var prompts embed.FS

const noExternalTools = "Use only the evidence provided in this prompt. Do not call external tools or search the web; if something is missing, say so explicitly."
const analystPreamble = "You are a helpful AI assistant, collaborating with other assistants. Use the provided tools to progress towards answering the question. If you are unable to fully answer, that's OK; another assistant with different tools will help where you left off. Execute what you can to make progress. Report what your tools support; another agent decides the trade. You have access to the following tools: %s. Today's date is %s; treat it as 'now' for all analysis and tool-call date ranges. %s\n%s"
const missingPortfolio = "Portfolio context: not provided. You do not know the caller's current holdings or cash, so do not assume a flat book; give direction and sizing guidance in terms the caller can apply to their own position."

type SentimentSource interface {
	News(context.Context, string, string, string) (string, error)
	StockTwits(context.Context, string, string, string) (string, error)
	Reddit(context.Context, string, string, string) (string, error)
}
type Factory struct {
	Quick, Deep llm.Client
	Config      config.Config
	Sources     SentimentSource
	Schemas     map[string]tools.Spec
	ToolSpecs   map[string]tools.Spec
}

func New(quick, deep llm.Client, c config.Config, sources SentimentSource) (*Factory, error) {
	b, e := prompts.ReadFile("prompts/schemas.json")
	if e != nil {
		return nil, e
	}
	f := &Factory{Quick: quick, Deep: deep, Config: c, Sources: sources}
	if e = json.Unmarshal(b, &f.Schemas); e != nil {
		return nil, e
	}
	b, e = prompts.ReadFile("prompts/response_schemas.json")
	if e != nil {
		return nil, e
	}
	var schemas map[string]json.RawMessage
	if e = json.Unmarshal(b, &schemas); e != nil {
		return nil, e
	}
	for name, raw := range schemas {
		spec := f.Schemas[name]
		spec.Function.ResponseSchema = raw
		f.Schemas[name] = spec
	}
	f.ToolSpecs, e = tools.Specs()
	return f, e
}
func RenderTemplate(name string, vars map[string]string) (string, error) {
	data, e := prompts.ReadFile("prompts/" + name + ".txt")
	if e != nil {
		return "", e
	}
	var missing string
	result := regexp.MustCompile(`\{\{([^{}]+)\}\}`).ReplaceAllStringFunc(string(data), func(s string) string {
		key := s[2 : len(s)-2]
		if v, ok := vars[key]; ok {
			return v
		}
		missing = key
		return s
	})
	if missing != "" {
		return "", fmt.Errorf("missing prompt variable %s in %s", missing, name)
	}
	return result, nil
}
func InstrumentContext(s state.State) string {
	if strings.TrimSpace(s.InstrumentContext) != "" {
		return s.InstrumentContext
	}
	label := "instrument"
	if s.AssetType == "crypto" {
		label = "asset"
	}
	text := fmt.Sprintf("The %s to analyze is `%s`. Use this exact ticker in every tool call, report, and recommendation, preserving any exchange suffix (e.g. `.TO`, `.L`, `.HK`, `.T`, `-USD`).", label, s.CompanyOfInterest)
	if s.AssetType == "crypto" {
		text += " Treat it as a crypto asset rather than a company, and do not assume company fundamentals are available."
	}
	return text
}
func report(text, source string) string {
	if s := strings.TrimSpace(text); s != "" {
		return s
	}
	return fmt.Sprintf("(No %s report in this run: it is not available, not an empty finding.)", source)
}
func opponent(text, name string) string {
	if s := strings.TrimSpace(text); s != "" {
		return s
	}
	return fmt.Sprintf("(The %s has not spoken yet — open the debate with your own case.)", name)
}
func (f *Factory) vars(s state.State) map[string]string {
	language := ""
	if strings.ToLower(strings.TrimSpace(f.Config.OutputLanguage)) != "english" {
		language = " Write your entire response in " + f.Config.OutputLanguage + "."
	}
	portfolio := s.PortfolioContext
	if strings.TrimSpace(portfolio) == "" {
		portfolio = missingPortfolio
	}
	target, asset, fundamentals := "stock", "company", "Company fundamentals report"
	if s.AssetType != "stock" && s.AssetType != "" {
		target, asset, fundamentals = "asset", "asset", "Asset fundamentals report (may be unavailable for crypto)"
	}
	return map[string]string{"language": language, "get_language_instruction()": language, "NO_EXTERNAL_TOOLS": noExternalTools, "instrument_context": InstrumentContext(s), "portfolio_context": portfolio, "target_label": target, "asset_label": asset, "fundamentals_label": fundamentals, "company_name": s.CompanyOfInterest, "ticker": s.CompanyOfInterest, "current_date": s.TradeDate, "end_date": s.TradeDate, "market_research_report": report(s.MarketReport, "market"), "sentiment_report": report(s.SentimentReport, "sentiment"), "news_report": report(s.NewsReport, "news"), "fundamentals_report": report(s.FundamentalsReport, "fundamentals"), "investment_plan": s.InvestmentPlan, "research_plan": s.InvestmentPlan, "trader_plan": s.TraderInvestmentPlan, "trader_decision": s.TraderInvestmentPlan}
}

var AnalystTools = map[string][]string{"market": {"get_stock_data", "get_indicators", "get_verified_market_snapshot"}, "social": {"get_news"}, "news": {"get_news", "get_global_news", "get_macro_indicators", "get_prediction_markets"}, "fundamentals": {"get_fundamentals", "get_balance_sheet", "get_cashflow", "get_income_statement"}}

func (f *Factory) structured(ctx context.Context, client llm.Client, name string, messages []model.Message) (string, error) {
	req, buildErr := llm.StructuredRequest(client, messages, f.Schemas[name])
	if buildErr != nil {
		return "", buildErr
	}
	resp, err := client.Chat(ctx, req)
	if err == nil {
		var data []byte
		if len(req.ResponseFormat) > 0 {
			text := strings.TrimSpace(resp.Message.Content.String())
			if strings.HasPrefix(text, "```") {
				if i := strings.Index(text, "\n"); i >= 0 {
					text = text[i+1:]
				}
				if i := strings.LastIndex(text, "```"); i >= 0 {
					text = text[:i]
				}
			}
			data = []byte(text)
		} else if len(resp.Message.ToolCalls) > 0 && resp.Message.ToolCalls[0].Name == name {
			data, err = json.Marshal(resp.Message.ToolCalls[0].Args)
		} else {
			err = fmt.Errorf("structured output returned no parsed %s result", name)
		}
		if err == nil {
			var output string
			output, err = renderStructured(name, data)
			if err == nil {
				return output, nil
			}
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	slog.Warn("structured output failed; retrying once as free text", "schema", name, "error", err)
	resp, err = client.Chat(ctx, llm.ChatRequest{Messages: messages})
	if err != nil {
		return "", err
	}
	return resp.Message.Content.String(), nil
}
func (f *Factory) Analyst(key string) runtime.Node {
	return func(ctx context.Context, s state.State) (state.Update, error) {
		if key == "social" {
			return f.sentiment(ctx, s)
		}
		v := f.vars(s)
		body, e := RenderTemplate(key+"_analyst_system_message", v)
		if e != nil {
			return state.Update{}, e
		}
		names, ok := AnalystTools[key]
		if !ok {
			return state.Update{}, fmt.Errorf("unknown analyst: %s", key)
		}
		specs := []tools.Spec{}
		for _, n := range names {
			specs = append(specs, f.ToolSpecs[n])
		}
		system := fmt.Sprintf(analystPreamble, strings.Join(names, ", "), s.TradeDate, InstrumentContext(s), body)
		messages := append([]model.Message{model.NewMessage("system", system)}, s.Messages...)
		response, e := f.Quick.Chat(ctx, llm.ChatRequest{Messages: messages, Tools: specs})
		if e != nil {
			return state.Update{}, e
		}
		text := ""
		if len(response.Message.ToolCalls) == 0 {
			text = response.Message.Content.String()
		}
		u := state.Update{Messages: []model.Message{response.Message}}
		switch key {
		case "market":
			u.MarketReport = &text
		case "news":
			u.NewsReport = &text
		case "fundamentals":
			u.FundamentalsReport = &text
		}
		return u, nil
	}
}
func (f *Factory) sentiment(ctx context.Context, s state.State) (state.Update, error) {
	if f.Sources == nil {
		return state.Update{}, fmt.Errorf("sentiment sources are not configured")
	}
	date, e := time.Parse("2006-01-02", s.TradeDate)
	if e != nil {
		return state.Update{}, e
	}
	start := date.AddDate(0, 0, -7).Format("2006-01-02")
	news, e := f.Sources.News(ctx, s.CompanyOfInterest, start, s.TradeDate)
	if e != nil {
		return state.Update{}, e
	}
	stocktwits, e := f.Sources.StockTwits(ctx, s.CompanyOfInterest, start, s.TradeDate)
	if e != nil {
		return state.Update{}, e
	}
	reddit, e := f.Sources.Reddit(ctx, s.CompanyOfInterest, start, s.TradeDate)
	if e != nil {
		return state.Update{}, e
	}
	v := f.vars(s)
	v["start_date"] = start
	v["news_block"] = news
	v["stocktwits_block"] = stocktwits
	v["reddit_block"] = reddit
	body, e := RenderTemplate("sentiment_analyst_system_message", v)
	if e != nil {
		return state.Update{}, e
	}
	system := fmt.Sprintf("You are a helpful AI assistant, collaborating with other assistants. Report what your tools support; another agent decides the trade. Today's date is %s; treat it as 'now' for all analysis. %s %s\n%s", s.TradeDate, InstrumentContext(s), noExternalTools, body)
	msgs := append([]model.Message{model.NewMessage("system", system)}, s.Messages...)
	text, e := f.structured(ctx, f.Quick, "SentimentReport", msgs)
	return state.Update{Messages: []model.Message{model.NewMessage("ai", text)}, SentimentReport: &text}, e
}
func (f *Factory) Researcher(bull bool) runtime.Node {
	return func(ctx context.Context, s state.State) (state.Update, error) {
		d := s.InvestmentDebate
		v := f.vars(s)
		stem, label, other := "bear_researcher", "Bear Analyst", "bull analyst"
		if bull {
			stem, label, other = "bull_researcher", "Bull Analyst", "bear analyst"
		}
		v["history"] = d.History
		v["current_response"] = opponent(d.CurrentResponse, other)
		prompt, e := RenderTemplate(stem+"_prompt", v)
		if e != nil {
			return state.Update{}, e
		}
		r, e := f.Quick.Chat(ctx, llm.ChatRequest{Messages: []model.Message{model.NewMessage("human", prompt)}})
		if e != nil {
			return state.Update{}, e
		}
		argument := label + ": " + r.Message.Content.String()
		d.History += "\n" + argument
		d.CurrentResponse = argument
		d.Count++
		d.JudgeDecision = ""
		if bull {
			d.BullHistory += "\n" + argument
		} else {
			d.BearHistory += "\n" + argument
		}
		return state.Update{InvestmentDebate: &d}, nil
	}
}
func (f *Factory) ResearchManager() runtime.Node {
	return func(ctx context.Context, s state.State) (state.Update, error) {
		v := f.vars(s)
		v["history"] = s.InvestmentDebate.History
		prompt, e := RenderTemplate("research_manager_prompt", v)
		if e != nil {
			return state.Update{}, e
		}
		text, e := f.structured(ctx, f.Deep, "ResearchPlan", []model.Message{model.NewMessage("human", prompt)})
		if e != nil {
			return state.Update{}, e
		}
		d := s.InvestmentDebate
		d.JudgeDecision = text
		d.CurrentResponse = text
		return state.Update{InvestmentDebate: &d, InvestmentPlan: &text}, nil
	}
}
func (f *Factory) Trader() runtime.Node {
	return func(ctx context.Context, s state.State) (state.Update, error) {
		v := f.vars(s)
		v["grounding"] = ""
		v["report_section"] = ""
		if report := strings.TrimSpace(s.MarketReport); report != "" {
			v["grounding"] = "Ground concrete price levels (entry, stop-loss, position sizing) in the technical market report's price structure -- current price, support/resistance, ATR, and volatility -- and use the research plan for direction and strategy. "
			v["report_section"] = "Technical Market Report:\n" + report + "\n\n"
		}
		system, e := RenderTemplate("trader_system", v)
		if e != nil {
			return state.Update{}, e
		}
		user, e := RenderTemplate("trader_user", v)
		if e != nil {
			return state.Update{}, e
		}
		text, e := f.structured(ctx, f.Quick, "TraderProposal", []model.Message{model.NewMessage("system", system), model.NewMessage("user", user)})
		return state.Update{Messages: []model.Message{model.NewMessage("ai", text)}, TraderInvestmentPlan: &text, Sender: state.Ptr("Trader")}, e
	}
}
func (f *Factory) RiskDebator(kind string) runtime.Node {
	return func(ctx context.Context, s state.State) (state.Update, error) {
		d := s.RiskDebate
		v := f.vars(s)
		v["history"] = d.History
		v["current_aggressive_response"] = opponent(d.CurrentAggressiveResponse, "aggressive analyst")
		v["current_conservative_response"] = opponent(d.CurrentConservativeResponse, "conservative analyst")
		v["current_neutral_response"] = opponent(d.CurrentNeutralResponse, "neutral analyst")
		prompt, e := RenderTemplate(kind+"_debator_prompt", v)
		if e != nil {
			return state.Update{}, e
		}
		r, e := f.Quick.Chat(ctx, llm.ChatRequest{Messages: []model.Message{model.NewMessage("human", prompt)}})
		if e != nil {
			return state.Update{}, e
		}
		label := strings.ToUpper(kind[:1]) + kind[1:]
		argument := label + " Analyst: " + r.Message.Content.String()
		d.History += "\n" + argument
		d.LatestSpeaker = label
		d.Count++
		d.JudgeDecision = ""
		switch kind {
		case "aggressive":
			d.AggressiveHistory += "\n" + argument
			d.CurrentAggressiveResponse = argument
		case "conservative":
			d.ConservativeHistory += "\n" + argument
			d.CurrentConservativeResponse = argument
		case "neutral":
			d.NeutralHistory += "\n" + argument
			d.CurrentNeutralResponse = argument
		default:
			return state.Update{}, fmt.Errorf("unknown risk analyst %s", kind)
		}
		return state.Update{RiskDebate: &d}, nil
	}
}
func (f *Factory) PortfolioManager() runtime.Node {
	return func(ctx context.Context, s state.State) (state.Update, error) {
		v := f.vars(s)
		v["history"] = s.RiskDebate.History
		v["lessons_line"] = ""
		if s.PastContext != "" {
			v["lessons_line"] = "- Lessons from prior decisions and outcomes:\n" + s.PastContext + "\n"
		}
		prompt, e := RenderTemplate("portfolio_manager_prompt", v)
		if e != nil {
			return state.Update{}, e
		}
		text, e := f.structured(ctx, f.Deep, "PortfolioDecision", []model.Message{model.NewMessage("human", prompt)})
		if e != nil {
			return state.Update{}, e
		}
		d := s.RiskDebate
		d.JudgeDecision = text
		d.LatestSpeaker = "Judge"
		return state.Update{RiskDebate: &d, FinalTradeDecision: &text}, nil
	}
}
func ClearMessages(ctx context.Context, s state.State) (state.Update, error) {
	messages := make([]model.Message, 0, len(s.Messages)+1)
	for _, m := range s.Messages {
		messages = append(messages, model.Message{Type: "remove", ID: m.ID})
	}
	messages = append(messages, model.NewMessage("human", fmt.Sprintf("Proceed with your assigned analysis for this workflow. %s The analysis date is %s.", InstrumentContext(s), s.TradeDate)))
	return state.Update{Messages: messages}, nil
}
