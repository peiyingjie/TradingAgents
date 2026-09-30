package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"tradingagents/internal/graph"
)

var analystOrder = []string{"market", "social", "news", "fundamentals"}

func (t terminal) selectAnalysts(asset string, defaults []string) ([]string, error) {
	options := []menuOption{}
	for _, key := range analystOrder {
		if asset != "crypto" || key != "fundamentals" {
			options = append(options, menuOption{graph.AnalystSpecs[key].AgentNode, key})
		}
	}
	var value string
	var err error
	if t.interactive() {
		value, err = t.menu("Select Your [Analysts Team]", options, defaults, true)
	} else {
		value, err = t.ask("Analysts (comma-separated: market,social,news,fundamentals)", strings.Join(defaults, ","))
	}
	if err != nil {
		return nil, err
	}
	chosen := split(strings.ToLower(value))
	if _, err = graph.Plan(chosen); err != nil {
		return nil, err
	}
	selected := []string{}
	for _, o := range options {
		if slices.Contains(chosen, o.Value) {
			selected = append(selected, o.Value)
		}
	}
	if _, err = graph.Plan(selected); err != nil {
		return nil, err
	}
	return selected, nil
}
func (t terminal) selectLanguage(def string) (string, error) {
	options := []menuOption{{"English (default)", "English"}, {"Chinese (中文)", "Chinese"}, {"Japanese (日本語)", "Japanese"}, {"Korean (한국어)", "Korean"}, {"Hindi (हिन्दी)", "Hindi"}, {"Spanish (Español)", "Spanish"}, {"Portuguese (Português)", "Portuguese"}, {"French (Français)", "French"}, {"German (Deutsch)", "German"}, {"Arabic (العربية)", "Arabic"}, {"Russian (Русский)", "Russian"}, {"Custom language", "custom"}}
	var value string
	var err error
	if t.interactive() {
		value, err = t.choose("Select Output Language", options, def)
	} else {
		value, err = t.ask("Output language", def)
	}
	if errors.Is(err, ErrCancelled) {
		return "English", nil
	}
	if err != nil {
		return "", err
	}
	if value == "custom" {
		value, err = t.requireText("Enter language name", "")
		if errors.Is(err, ErrCancelled) {
			return "English", nil
		}
	}
	return value, err
}
func (t terminal) selectProvider(def string) (string, error) {
	options := []menuOption{{"OpenAI", "openai"}, {"Google", "google"}, {"Anthropic", "anthropic"}, {"xAI", "xai"}, {"DeepSeek", "deepseek"}, {"Qwen", "qwen"}, {"GLM", "glm"}, {"MiniMax", "minimax"}, {"OpenRouter", "openrouter"}, {"Mistral", "mistral"}, {"Kimi (Moonshot)", "kimi"}, {"Groq", "groq"}, {"NVIDIA NIM", "nvidia"}, {"Azure OpenAI", "azure"}, {"Amazon Bedrock", "bedrock"}, {"Ollama", "ollama"}, {"OpenAI-compatible (vLLM, LM Studio, llama.cpp, custom relay)", "openai_compatible"}}
	provider, err := t.choose("LLM provider", options, strings.TrimSuffix(def, "-cn"))
	if err != nil {
		return "", err
	}
	switch provider {
	case "qwen", "glm", "minimax":
		return t.choose(fmt.Sprintf("Select %s region / platform", provider), []menuOption{{"International / Global", provider}, {"China", provider + "-cn"}}, provider)
	}
	return provider, nil
}
