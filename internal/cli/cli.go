package cli

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/joho/godotenv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"tradingagents/internal/backtest"
	"tradingagents/internal/callbacks"
	"tradingagents/internal/config"
	"tradingagents/internal/dataflows"
	"tradingagents/internal/graph"
	"tradingagents/internal/llm"
	"tradingagents/internal/memory"
	"tradingagents/internal/portfolio"
	"tradingagents/internal/runtime"
)

//go:embed models.json
var modelsJSON []byte

type preferences struct {
	Language string   `json:"output_language,omitempty"`
	Analysts []string `json:"analysts,omitempty"`
	Depth    int      `json:"research_depth,omitempty"`
	Provider string   `json:"llm_provider,omitempty"`
	Quick    string   `json:"quick_think_llm,omitempty"`
	Deep     string   `json:"deep_think_llm,omitempty"`
	Backend  string   `json:"backend_url,omitempty"`
}
type terminal struct {
	Reader  *bufio.Reader
	Input   io.Reader
	Out     io.Writer
	Context context.Context
}

func (t terminal) ask(label, def string) (string, error) {
	if t.interactive() {
		return t.readText(label, def, false)
	}
	if err := t.context().Err(); err != nil {
		return "", err
	}
	if _, e := fmt.Fprintf(t.Out, "%s [%s]: ", label, def); e != nil {
		return "", e
	}
	v, e := t.Reader.ReadString('\n')
	if e = plainInputError(v, e); e != nil {
		return "", fmt.Errorf("input ended at %s: %w", label, e)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		v = def
	}
	return v, nil
}
func split(s string) []string {
	out := []string{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func dotenvPath() (string, error) {
	cwd, e := os.Getwd()
	if e != nil {
		return "", e
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		p := filepath.Join(dir, ".env")
		if _, e := os.Stat(p); e == nil {
			return p, nil
		} else if !os.IsNotExist(e) {
			return "", e
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return filepath.Join(cwd, ".env"), nil
}
func Run(ctx context.Context, args []string, input io.Reader, out, stderr io.Writer) error {
	// Help stays available before credentials/config validation.
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		if slices.Contains(args, "backtest") {
			fmt.Fprintln(out, "Usage: tradingagents backtest TICKERS --start YYYY-MM-DD --end YYYY-MM-DD [--every 7] [--analysts market,social,news,fundamentals] [--asset-type stock|crypto] [--portfolio FILE] [--run-id ID]")
		} else {
			fmt.Fprintln(out, "Usage: tradingagents [--checkpoint|--no-checkpoint] [--clear-checkpoints] [--portfolio FILE]\n\nRun an interactive analysis.\n\nCommands:\n  backtest  Score past decisions over a grid of tickers and dates.\n\nOptions:\n  --checkpoint          Save state after each node and resume interrupted runs\n  --no-checkpoint       Disable checkpointing\n  --clear-checkpoints   Clear saved checkpoints before running\n  --portfolio FILE      JSON holdings and cash\n  --help                Show help")
		}
		return nil
	}
	path, e := dotenvPath()
	if e != nil {
		return e
	}
	if e = godotenv.Load(path); e != nil && !os.IsNotExist(e) {
		return e
	}
	c, e := config.Default()
	if e != nil {
		return e
	}
	if len(args) > 0 && args[0] == "backtest" {
		return runBacktest(ctx, args[1:], c, out, stderr)
	}
	fs := flag.NewFlagSet("tradingagents", flag.ContinueOnError)
	fs.SetOutput(stderr)
	checkpoint := fs.Bool("checkpoint", false, "")
	noCheckpoint := fs.Bool("no-checkpoint", false, "")
	clear := fs.Bool("clear-checkpoints", false, "")
	portfolioPath := fs.String("portfolio", "", "")
	if e = fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unknown command %q", fs.Arg(0))
	}
	if *checkpoint && *noCheckpoint {
		return fmt.Errorf("--checkpoint and --no-checkpoint cannot be combined")
	}
	if *checkpoint {
		c.CheckpointEnabled = true
	}
	if *noCheckpoint {
		c.CheckpointEnabled = false
	}
	if *clear {
		n, e := graph.ClearCheckpoints(c.DataCacheDir)
		if e != nil {
			return e
		}
		fmt.Fprintf(out, "Cleared %d checkpoint(s).\n", n)
	}
	var book *portfolio.Portfolio
	if *portfolioPath != "" {
		book, e = portfolio.Load(*portfolioPath)
		if e != nil {
			return e
		}
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	prefsPath := filepath.Join(home, ".tradingagents", "cli_prefs.json")
	var prefs preferences
	if b, e := os.ReadFile(prefsPath); e == nil {
		if e = json.Unmarshal(b, &prefs); e != nil {
			slog.Debug("ignoring unreadable CLI preferences", "error", e)
		}
	}
	fmt.Fprintln(out, "TradingAgents: Multi-Agents LLM Financial Trading Framework - CLI\nI. Analyst Team → II. Research Team → III. Trader → IV. Risk Management → V. Portfolio Management")
	termUI := terminal{Reader: bufio.NewReader(input), Input: input, Out: out, Context: ctx}
	if termUI.interactive() {
		restore, err := enableANSI(out.(*os.File))
		if err != nil {
			return err
		}
		defer restore()
	}
	if e = termUI.showAnnouncements(ctx); e != nil {
		return e
	}
	ticker, e := termUI.ask("Ticker symbol (e.g. SPY, 0700.HK, BTC-USD)", "SPY")
	if e != nil {
		return e
	}
	data, e := dataflows.New(c)
	if e != nil {
		return e
	}
	ticker = data.Normalize(ticker)
	if _, e = dataflows.SafeComponent(ticker); e != nil {
		return e
	}
	asset := "stock"
	for _, suffix := range []string{"-USD", "-USDT", "-USDC", "-BTC", "-ETH"} {
		if strings.HasSuffix(ticker, suffix) {
			asset = "crypto"
		}
	}
	date, e := termUI.ask("Analysis date (YYYY-MM-DD)", time.Now().Format("2006-01-02"))
	if e != nil {
		return e
	}
	if _, e = dataflows.ParseDate(date); e != nil {
		return e
	}
	if date > time.Now().Format("2006-01-02") {
		return fmt.Errorf("analysis date cannot be in the future")
	}
	if os.Getenv("TRADINGAGENTS_OUTPUT_LANGUAGE") == "" {
		def := c.OutputLanguage
		if prefs.Language != "" {
			def = prefs.Language
		}
		c.OutputLanguage, e = termUI.selectLanguage(def)
		if e != nil {
			return e
		}
	}
	selected := []string{"market", "social", "news", "fundamentals"}
	if len(prefs.Analysts) > 0 {
		selected = nil
		for _, key := range prefs.Analysts {
			if _, ok := graph.AnalystSpecs[key]; ok {
				selected = append(selected, key)
			}
		}
	}
	if asset == "crypto" {
		selected = slices.DeleteFunc(selected, func(s string) bool { return s == "fundamentals" })
	}
	if len(selected) == 0 {
		selected = []string{"market", "social", "news"}
	}
	selected, e = termUI.selectAnalysts(asset, selected)
	if e != nil {
		return e
	}
	if _, e = graph.Plan(selected); e != nil {
		return e
	}
	depth := 1
	if prefs.Depth == 1 || prefs.Depth == 3 || prefs.Depth == 5 {
		depth = prefs.Depth
	}
	if os.Getenv("TRADINGAGENTS_MAX_DEBATE_ROUNDS") == "" || os.Getenv("TRADINGAGENTS_MAX_RISK_ROUNDS") == "" {
		var v string
		if termUI.interactive() {
			v, e = termUI.choose("Research depth", []menuOption{{"Shallow - Quick research", "1"}, {"Medium - Moderate debate rounds", "3"}, {"Deep - Comprehensive research", "5"}}, strconv.Itoa(depth))
		} else {
			v, e = termUI.ask("Research depth (1 shallow / 3 medium / 5 deep)", strconv.Itoa(depth))
		}
		if e != nil {
			return e
		}
		depth, e = strconv.Atoi(v)
		if e != nil || !slices.Contains([]int{1, 3, 5}, depth) {
			return fmt.Errorf("research depth must be 1, 3, or 5")
		}
	}
	if os.Getenv("TRADINGAGENTS_MAX_DEBATE_ROUNDS") == "" {
		c.MaxDebateRounds = depth
	}
	if os.Getenv("TRADINGAGENTS_MAX_RISK_ROUNDS") == "" {
		c.MaxRiskDiscussRounds = depth
	}
	providerEnv := os.Getenv("TRADINGAGENTS_LLM_PROVIDER") != ""
	if !providerEnv {
		def := c.LLMProvider
		if _, ok := llm.Providers[prefs.Provider]; ok {
			def = prefs.Provider
		}
		c.LLMProvider, e = termUI.selectProvider(def)
		if e != nil {
			return e
		}
		c.LLMProvider = strings.ToLower(c.LLMProvider)
	}
	spec, ok := llm.Providers[c.LLMProvider]
	if !ok {
		return fmt.Errorf("unsupported LLM provider %s", c.LLMProvider)
	}
	if c.BackendURL == "" {
		c.BackendURL = spec.URL
		if c.LLMProvider == "ollama" && os.Getenv("OLLAMA_BASE_URL") != "" {
			c.BackendURL = os.Getenv("OLLAMA_BASE_URL")
		}
		if c.LLMProvider == "openai_compatible" {
			def := ""
			if prefs.Provider == c.LLMProvider {
				def = prefs.Backend
			}
			c.BackendURL, e = termUI.ask("OpenAI-compatible backend URL", def)
			if e != nil {
				return e
			}
			if c.BackendURL == "" {
				return fmt.Errorf("backend URL is required")
			}
			if !strings.HasPrefix(c.BackendURL, "http://") && !strings.HasPrefix(c.BackendURL, "https://") {
				return fmt.Errorf("backend URL must start with http:// or https://")
			}
		}
	}
	if !spec.Optional && os.Getenv(spec.KeyEnv) == "" && !(c.LLMProvider == "google" && os.Getenv("GEMINI_API_KEY") != "") {
		fmt.Fprintf(out, "%s is missing. Enter API key: ", spec.KeyEnv)
		var key string
		if termUI.interactive() {
			key, e = termUI.readText(spec.KeyEnv+" API key", "", true)
			if e != nil {
				return e
			}
		} else {
			key, e = termUI.Reader.ReadString('\n')
			if e != nil {
				return e
			}
			key = strings.TrimSpace(key)
			if err := plainInputError(key, nil); err != nil {
				return err
			}
		}
		if key == "" {
			return fmt.Errorf("%s is required", spec.KeyEnv)
		}
		if e = os.Setenv(spec.KeyEnv, key); e != nil {
			return e
		}
		existing, err := godotenv.Read(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if existing == nil {
			existing = map[string]string{}
		}
		existing[spec.KeyEnv] = key
		if e = godotenv.Write(existing, path); e != nil {
			return e
		}
	}
	if os.Getenv("TRADINGAGENTS_QUICK_THINK_LLM") == "" && os.Getenv("TRADINGAGENTS_DEEP_THINK_LLM") == "" {
		quick, deep := "", ""
		if prefs.Provider == c.LLMProvider {
			quick, deep = prefs.Quick, prefs.Deep
		}
		c.QuickThinkLLM, e = termUI.selectModel(ctx, c.LLMProvider, "quick", quick)
		if e != nil {
			return e
		}
		c.DeepThinkLLM, e = termUI.selectModel(ctx, c.LLMProvider, "deep", deep)
		if e != nil {
			return e
		}
	}
	if !providerEnv {
		switch c.LLMProvider {
		case "google":
			if os.Getenv("TRADINGAGENTS_GOOGLE_THINKING_LEVEL") == "" {
				c.GoogleThinkingLevel, e = termUI.choose("Gemini thinking level", []menuOption{{"Enable Thinking (recommended)", "high"}, {"Minimal/Disable Thinking", "minimal"}}, "high")
			}
		case "openai":
			if os.Getenv("TRADINGAGENTS_OPENAI_REASONING_EFFORT") == "" {
				c.OpenAIReasoningEffort, e = termUI.choose("OpenAI reasoning effort", []menuOption{{"Medium (Default)", "medium"}, {"High (More thorough)", "high"}, {"Low (Faster)", "low"}}, "medium")
			}
		case "anthropic":
			if os.Getenv("TRADINGAGENTS_ANTHROPIC_EFFORT") == "" {
				c.AnthropicEffort, e = termUI.choose("Claude effort", []menuOption{{"High (recommended)", "high"}, {"Medium (balanced)", "medium"}, {"Low (faster, cheaper)", "low"}}, "high")
			}
		}
		if e != nil {
			return e
		}
	}
	prefs = preferences{c.OutputLanguage, selected, depth, c.LLMProvider, c.QuickThinkLLM, c.DeepThinkLLM, c.BackendURL}
	b, e := json.MarshalIndent(prefs, "", "  ")
	if e != nil {
		return e
	}
	if e = dataflows.AtomicWrite(prefsPath, b); e != nil {
		slog.Warn("could not save CLI preferences", "error", e)
	}
	g, e := graph.New(c, selected)
	if e != nil {
		return e
	}
	timing, e := graph.NewAnalystWallTimeTracker(selected)
	if e != nil {
		return e
	}
	if e = timing.MarkStarted(selected[0], time.Now()); e != nil {
		return e
	}
	view := newProgressView(selected)
	g.OnEvent = func(ev runtime.Event) error {
		timing.Sync(ev.State, time.Now())
		view.update(ev)
		if ev.Node != "" && !termUI.interactive() {
			_, err := fmt.Fprintf(out, "[%d] %s\n", ev.Step, ev.Node)
			return err
		}
		return nil
	}
	var calls, toolCalls, inTokens, outTokens atomic.Int64
	stats := func() string {
		return fmt.Sprintf("LLM calls: %d | Tool calls: %d | Tokens: %d input, %d output", calls.Load(), toolCalls.Load(), inTokens.Load(), outTokens.Load())
	}
	stopDisplay := func() error { return nil }
	if termUI.interactive() {
		stopDisplay = view.start(out.(*os.File), stats)
		defer stopDisplay()
	}
	ctx = callbacks.WithHandlers(ctx, func(event callbacks.Event) {
		switch event.Kind {
		case "on_chat_model_start":
			calls.Add(1)
		case "on_tool_start":
			toolCalls.Add(1)
		case "on_llm_end":
			if u := event.Response.Usage; u != nil {
				inTokens.Add(int64(u.InputTokens))
				outTokens.Add(int64(u.OutputTokens))
			}
		}
	})
	final, signal, e := g.Propagate(ctx, ticker, date, asset, book)
	displayErr := stopDisplay()
	fmt.Fprintln(out, stats())
	fmt.Fprintln(out, timing.Summary())
	if e != nil {
		return e
	}
	if displayErr != nil {
		return displayErr
	}
	if signal == "REVIEW" {
		fmt.Fprintln(out, "No rating could be read from the final decision, so this run is recorded for review rather than as a position. Re-run, or read the decision text below and judge it yourself.")
	}
	fmt.Fprintln(out, final.FinalTradeDecision)
	fmt.Fprintf(out, "\nSignal: %s\n", signal)
	return termUI.offerReport(final, ticker, c.ResultsDir, time.Now())
}
func runBacktest(ctx context.Context, args []string, c config.Config, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	start := fs.String("start", "", "")
	end := fs.String("end", "", "")
	every := fs.Int("every", 7, "")
	analysts := fs.String("analysts", "market,social,news,fundamentals", "")
	asset := fs.String("asset-type", "stock", "")
	portfolioPath := fs.String("portfolio", "", "")
	runID := fs.String("run-id", "", "")
	flags, positionals := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positionals = append(positionals, a)
		}
	}
	if e := fs.Parse(flags); e != nil {
		return e
	}
	if len(positionals) != 1 {
		return fmt.Errorf("backtest requires comma-separated tickers")
	}
	tickers := split(positionals[0])
	if len(tickers) == 0 {
		return fmt.Errorf("no ticker to analyze")
	}
	dates, e := backtest.Grid(*start, *end, *every, time.Now())
	if e != nil {
		return e
	}
	var book *portfolio.Portfolio
	if *portfolioPath != "" {
		book, e = portfolio.Load(*portfolioPath)
		if e != nil {
			return e
		}
	}
	result, e := backtest.Run(ctx, tickers, dates, c, *asset, book, split(strings.ToLower(*analysts)), *runID)
	if e != nil {
		return e
	}
	log := memory.Log{Path: result.LogPath}
	entries, e := log.Entries()
	if e != nil {
		return e
	}
	fmt.Fprintln(out, backtest.Summarize(entries).Render())
	fmt.Fprintf(out, "\nRan %d cells, skipped %d. Log: %s\n", result.CellsRun, result.Skipped, result.LogPath)
	for _, f := range result.Failures {
		fmt.Fprintf(out, "failed: %s %s: %s\n", f.Ticker, f.Date, f.Reason)
	}
	for _, f := range result.SettlementFailures {
		fmt.Fprintf(out, "unsettled: %s: %s\n", f.Ticker, f.Reason)
	}
	return nil
}
