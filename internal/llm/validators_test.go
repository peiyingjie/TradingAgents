package llm

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"tradingagents/internal/config"
)

func TestModelValidationAndAdvisoryWarning(t *testing.T) {
	for _, test := range []struct {
		provider, model string
		valid           bool
	}{
		{"OPENAI", "gpt-5.4", true}, {"openai", "gpt-5.6-sol", true}, {"anthropic", "claude-opus-4-7", true},
		{"openai", "unknown-future-model", false}, {"google", "unknown-future-model", false},
		{"openrouter", "custom/model", true}, {"azure", "deployment", true}, {"ollama", "local", true}, {"qwen-cn", "qwen3.7-max", false},
	} {
		if got := ValidateModel(test.provider, test.model); got != test.valid {
			t.Errorf("%s/%s: %v", test.provider, test.model, got)
		}
	}
	var log bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	defer slog.SetDefault(old)
	t.Setenv("OPENAI_API_KEY", "fake-key")
	adapter, err := New(config.Config{LLMProvider: "openai"}, "unknown-future-model")
	if err != nil || adapter.Model != "unknown-future-model" || !strings.Contains(log.String(), "Continuing anyway.") {
		t.Fatal(adapter, err, log.String())
	}
	if strings.Contains(log.String(), "fake-key") {
		t.Fatal("credential exposed")
	}
	log.Reset()
	warnUnknownModel("openai", "gpt-5.4")
	if log.Len() != 0 {
		t.Fatal(log.String())
	}
}
