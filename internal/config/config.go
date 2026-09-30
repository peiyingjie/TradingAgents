package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed defaults.json
var defaults []byte

type Config struct {
	ProjectDir             string            `json:"project_dir"`
	ResultsDir             string            `json:"results_dir"`
	DataCacheDir           string            `json:"data_cache_dir"`
	MemoryLogPath          string            `json:"memory_log_path"`
	MemoryLogMaxEntries    *int              `json:"memory_log_max_entries"`
	LLMProvider            string            `json:"llm_provider"`
	DeepThinkLLM           string            `json:"deep_think_llm"`
	QuickThinkLLM          string            `json:"quick_think_llm"`
	BackendURL             string            `json:"backend_url"`
	GoogleThinkingLevel    string            `json:"google_thinking_level"`
	OpenAIReasoningEffort  string            `json:"openai_reasoning_effort"`
	AnthropicEffort        string            `json:"anthropic_effort"`
	Temperature            *float64          `json:"temperature"`
	LLMMaxRetries          *int              `json:"llm_max_retries"`
	MaxTokens              *int              `json:"max_tokens"`
	CheckpointEnabled      bool              `json:"checkpoint_enabled"`
	OutputLanguage         string            `json:"output_language"`
	MaxDebateRounds        int               `json:"max_debate_rounds"`
	MaxRiskDiscussRounds   int               `json:"max_risk_discuss_rounds"`
	MaxRecurLimit          int               `json:"max_recur_limit"`
	NewsArticleLimit       int               `json:"news_article_limit"`
	GlobalNewsArticleLimit int               `json:"global_news_article_limit"`
	GlobalNewsLookbackDays int               `json:"global_news_lookback_days"`
	GlobalNewsQueries      []string          `json:"global_news_queries"`
	DataVendors            map[string]string `json:"data_vendors"`
	ToolVendors            map[string]string `json:"tool_vendors"`
	HoldingPeriodDays      int               `json:"holding_period_days"`
	BenchmarkTicker        string            `json:"benchmark_ticker"`
	BenchmarkMap           map[string]string `json:"benchmark_map"`
}

func Default() (Config, error) {
	var c Config
	if err := json.Unmarshal(defaults, &c); err != nil {
		return c, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return c, err
	}
	c.ProjectDir = "tradingagents"
	c.ResultsDir = filepath.Join(home, ".tradingagents", "logs")
	c.DataCacheDir = filepath.Join(home, ".tradingagents", "cache")
	c.MemoryLogPath = filepath.Join(home, ".tradingagents", "memory", "trading_memory.md")
	return c, c.ApplyEnv(os.Getenv)
}

func (c *Config) ApplyEnv(get func(string) string) error {
	stringsEnv := map[string]*string{"LLM_PROVIDER": &c.LLMProvider, "DEEP_THINK_LLM": &c.DeepThinkLLM, "QUICK_THINK_LLM": &c.QuickThinkLLM, "LLM_BACKEND_URL": &c.BackendURL, "OUTPUT_LANGUAGE": &c.OutputLanguage, "BENCHMARK_TICKER": &c.BenchmarkTicker, "GOOGLE_THINKING_LEVEL": &c.GoogleThinkingLevel, "OPENAI_REASONING_EFFORT": &c.OpenAIReasoningEffort, "ANTHROPIC_EFFORT": &c.AnthropicEffort, "RESULTS_DIR": &c.ResultsDir, "CACHE_DIR": &c.DataCacheDir, "MEMORY_LOG_PATH": &c.MemoryLogPath}
	for key, p := range stringsEnv {
		if v := get("TRADINGAGENTS_" + key); v != "" {
			*p = v
		}
	}
	for key, p := range map[string]*int{"MAX_DEBATE_ROUNDS": &c.MaxDebateRounds, "MAX_RISK_ROUNDS": &c.MaxRiskDiscussRounds} {
		if v := get("TRADINGAGENTS_" + key); v != "" {
			n, e := strconv.Atoi(strings.TrimSpace(v))
			if e != nil {
				return fmt.Errorf("invalid TRADINGAGENTS_%s: %w", key, e)
			}
			*p = n
		}
	}
	for key, p := range map[string]**int{"LLM_MAX_RETRIES": &c.LLMMaxRetries, "MAX_TOKENS": &c.MaxTokens} {
		if v := get("TRADINGAGENTS_" + key); v != "" {
			n, e := strconv.Atoi(strings.TrimSpace(v))
			if e != nil {
				return fmt.Errorf("invalid TRADINGAGENTS_%s: %w", key, e)
			}
			*p = &n
		}
	}
	if v := get("TRADINGAGENTS_TEMPERATURE"); v != "" {
		n, e := strconv.ParseFloat(v, 64)
		if e != nil {
			return fmt.Errorf("invalid TRADINGAGENTS_TEMPERATURE: %w", e)
		}
		c.Temperature = &n
	}
	if v := get("TRADINGAGENTS_CHECKPOINT_ENABLED"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			c.CheckpointEnabled = true
		case "false", "0", "no", "off":
			c.CheckpointEnabled = false
		default:
			return fmt.Errorf("invalid TRADINGAGENTS_CHECKPOINT_ENABLED: %q", v)
		}
	}
	if c.LLMMaxRetries != nil && *c.LLMMaxRetries < 0 {
		return fmt.Errorf("llm_max_retries must be non-negative")
	}
	if c.MaxTokens != nil && *c.MaxTokens < 1 {
		return fmt.Errorf("max_tokens must be positive")
	}
	return nil
}
