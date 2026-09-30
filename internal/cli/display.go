package cli

import (
	"fmt"
	"golang.org/x/term"
	"golang.org/x/text/width"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"tradingagents/internal/graph"
	"tradingagents/internal/runtime"
	"tradingagents/internal/state"
	"unicode"
)

type progressView struct {
	mu           sync.Mutex
	names        []string
	statuses     map[string]string
	seen         map[string]bool
	messages     []string
	report       string
	reports      int
	totalReports int
	started      time.Time
}

func newProgressView(selected []string) *progressView {
	names := []string{}
	for _, key := range selected {
		names = append(names, graph.AnalystSpecs[key].AgentNode)
	}
	names = append(names, "Bull Researcher", "Bear Researcher", "Research Manager", "Trader", "Aggressive Analyst", "Neutral Analyst", "Conservative Analyst", "Portfolio Manager")
	statuses := map[string]string{}
	for _, name := range names {
		statuses[name] = "pending"
	}
	statuses[names[0]] = "in_progress"
	return &progressView{names: names, statuses: statuses, seen: map[string]bool{}, totalReports: len(selected) + 3, started: time.Now()}
}
func (v *progressView) update(ev runtime.Event) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := ev.State
	reports := map[string]string{"Market Analyst": s.MarketReport, "Sentiment Analyst": s.SentimentReport, "News Analyst": s.NewsReport, "Fundamentals Analyst": s.FundamentalsReport}
	v.reports = 0
	for name, report := range reports {
		if _, ok := v.statuses[name]; ok && report != "" {
			v.statuses[name] = "completed"
			v.reports++
		}
	}
	if s.InvestmentDebate.BullHistory != "" || s.InvestmentDebate.BearHistory != "" {
		v.statuses["Bull Researcher"] = "in_progress"
		v.statuses["Bear Researcher"] = "in_progress"
	}
	if s.InvestmentDebate.JudgeDecision != "" {
		for _, name := range []string{"Bull Researcher", "Bear Researcher", "Research Manager"} {
			v.statuses[name] = "completed"
		}
		v.reports++
	}
	if s.TraderInvestmentPlan != "" {
		v.statuses["Trader"] = "completed"
		v.reports++
	}
	if s.RiskDebate.AggressiveHistory != "" {
		v.statuses["Aggressive Analyst"] = "in_progress"
	}
	if s.RiskDebate.ConservativeHistory != "" {
		v.statuses["Conservative Analyst"] = "in_progress"
	}
	if s.RiskDebate.NeutralHistory != "" {
		v.statuses["Neutral Analyst"] = "in_progress"
	}
	if s.RiskDebate.JudgeDecision != "" {
		for _, name := range []string{"Aggressive Analyst", "Conservative Analyst", "Neutral Analyst", "Portfolio Manager"} {
			v.statuses[name] = "completed"
		}
		v.reports++
	}
	if _, ok := v.statuses[ev.Next]; ok && v.statuses[ev.Next] != "completed" {
		v.statuses[ev.Next] = "in_progress"
	}
	if ev.Next == runtime.End {
		for name := range v.statuses {
			v.statuses[name] = "completed"
		}
	}
	for _, m := range s.Messages {
		if m.ID != "" && v.seen[m.ID] {
			continue
		}
		if m.ID != "" {
			v.seen[m.ID] = true
		}
		kind := "System"
		switch m.Type {
		case "human":
			kind = "User"
		case "ai":
			kind = "Agent"
		case "tool":
			kind = "Data"
		}
		content := strings.TrimSpace(m.Content.String())
		if kind == "User" && content == "Continue" {
			kind = "Control"
		}
		if content != "" {
			v.messages = append(v.messages, kind+": "+content)
		}
		for _, call := range m.ToolCalls {
			v.messages = append(v.messages, "Tool: "+call.Name+fmt.Sprint(call.Args))
		}
	}
	if len(v.messages) > 12 {
		v.messages = v.messages[len(v.messages)-12:]
	}
	// Only changed report sections become the current report.
	for _, p := range []*string{ev.Update.MarketReport, ev.Update.SentimentReport, ev.Update.NewsReport, ev.Update.FundamentalsReport, ev.Update.InvestmentPlan, ev.Update.TraderInvestmentPlan, ev.Update.FinalTradeDecision} {
		if p != nil && *p != "" {
			v.report = *p
		}
	}
	if ev.Update.InvestmentDebate != nil && s.InvestmentDebate.History != "" {
		v.report = s.InvestmentDebate.History
		if s.InvestmentDebate.JudgeDecision != "" {
			v.report = s.InvestmentDebate.JudgeDecision
		}
	}
	if ev.Update.RiskDebate != nil && s.RiskDebate.History != "" {
		v.report = s.RiskDebate.History
		if s.RiskDebate.JudgeDecision != "" {
			v.report = s.RiskDebate.JudgeDecision
		}
	}
	if v.report == "" {
		for _, item := range reportSections(s) {
			if item.text != "" {
				v.report = item.text
			}
		}
	}
}
func safeLine(s string, width int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if cellWidth(s) > width {
		return prefixCells(s, max(0, width-3)) + "..."
	}
	return s
}
func runeWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}
func cellWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}
func prefixCells(s string, limit int) string {
	n := 0
	for i, r := range s {
		n += runeWidth(r)
		if n > limit {
			return s[:i]
		}
	}
	return s
}
func (v *progressView) frame(width, height int, stats string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	width = max(20, width-1)
	height = max(10, height)
	lines := []string{"Welcome to TradingAgents CLI — Tauric Research", "Progress"}
	completed := 0
	for _, name := range v.names {
		if v.statuses[name] == "completed" {
			completed++
		}
		lines = append(lines, fmt.Sprintf("  %-23s %s", name, v.statuses[name]))
	}
	lines = append(lines, "Messages & Tools")
	slots := max(0, min(4, height-len(lines)-6))
	for i := len(v.messages) - 1; i >= max(0, len(v.messages)-slots); i-- {
		lines = append(lines, v.messages[i])
	}
	lines = append(lines, "Current Report")
	report := v.report
	if report == "" {
		report = "Waiting for analysis report..."
	}
	for _, line := range strings.Split(report, "\n") {
		if len(lines) >= height-3 {
			break
		}
		lines = append(lines, line)
	}
	lines = append(lines, fmt.Sprintf("Agents: %d/%d | Reports: %d/%d | Elapsed: %s", completed, len(v.names), v.reports, v.totalReports, time.Since(v.started).Truncate(time.Second)), stats)
	if len(lines) > height-1 {
		lines = append(lines[:height-2], lines[len(lines)-1])
	}
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	for _, line := range lines {
		fmt.Fprintf(&b, "\x1b[36m%s\x1b[0m\r\n", safeLine(line, width))
	}
	return b.String()
}
func (v *progressView) start(out *os.File, stats func() string) func() error {
	done, finished := make(chan struct{}), make(chan struct{})
	var renderErr error
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	go func() {
		defer close(finished)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			width, height, err := term.GetSize(int(out.Fd()))
			if err != nil {
				width, height = 100, 35
			}
			if _, err = io.WriteString(out, v.frame(width, height, stats())); err != nil {
				renderErr = err
				return
			}
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	return func() error {
		once.Do(func() {
			close(done)
			<-finished
			_, err := fmt.Fprint(out, "\x1b[0m\x1b[?25h\x1b[?1049l")
			if renderErr == nil {
				renderErr = err
			}
		})
		return renderErr
	}
}

type reportSection struct{ title, text string }

func reportSections(s state.State) []reportSection {
	return []reportSection{
		{"Market Analyst", s.MarketReport}, {"Sentiment Analyst", s.SentimentReport}, {"News Analyst", s.NewsReport}, {"Fundamentals Analyst", s.FundamentalsReport},
		{"Bull Researcher", s.InvestmentDebate.BullHistory}, {"Bear Researcher", s.InvestmentDebate.BearHistory}, {"Research Manager", s.InvestmentDebate.JudgeDecision}, {"Trader", s.TraderInvestmentPlan},
		{"Aggressive Analyst", s.RiskDebate.AggressiveHistory}, {"Conservative Analyst", s.RiskDebate.ConservativeHistory}, {"Neutral Analyst", s.RiskDebate.NeutralHistory}, {"Portfolio Manager", s.FinalTradeDecision},
	}
}
