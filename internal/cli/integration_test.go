package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"tradingagents/internal/memory"
	"tradingagents/internal/tools"
	"tradingagents/pkg/model"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInteractiveCLIThroughToolsDecisionAndReports(t *testing.T) {
	b, e := os.ReadFile("../graph/testdata/python_graph.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct{ Samples map[string]json.RawMessage }
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	t.Chdir(root)
	if e = os.WriteFile(".env", nil, 0600); e != nil {
		t.Fatal(e)
	}
	for key, value := range map[string]string{"USERPROFILE": root, "HOME": root, "TRADINGAGENTS_LLM_PROVIDER": "openai_compatible", "TRADINGAGENTS_QUICK_THINK_LLM": "test-model", "TRADINGAGENTS_DEEP_THINK_LLM": "test-model", "TRADINGAGENTS_OUTPUT_LANGUAGE": "English", "TRADINGAGENTS_MAX_DEBATE_ROUNDS": "0", "TRADINGAGENTS_MAX_RISK_ROUNDS": "0", "TRADINGAGENTS_CHECKPOINT_ENABLED": "true", "TRADINGAGENTS_RESULTS_DIR": filepath.Join(root, "results"), "TRADINGAGENTS_CACHE_DIR": filepath.Join(root, "cache"), "TRADINGAGENTS_MEMORY_LOG_PATH": filepath.Join(root, "memory.md"), "TRADINGAGENTS_LLM_MAX_RETRIES": "0"} {
		t.Setenv(key, value)
	}
	var calls, charts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Messages []struct {
				Role    string
				Content model.Content
			}
			Tools []tools.Spec
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		message := map[string]any{"role": "assistant", "content": "**Rating**: Buy\nFixed evidence"}
		if len(req.Tools) > 0 {
			name := req.Tools[0].Function.Name
			if sample, ok := fixture.Samples[name]; ok {
				message["content"] = nil
				message["tool_calls"] = []any{map[string]any{"id": "structured", "type": "function", "function": map[string]any{"name": name, "arguments": string(sample)}}}
			} else if name == "get_stock_data" {
				if req.Messages[len(req.Messages)-1].Role != "tool" {
					message["content"] = nil
					message["tool_calls"] = []any{map[string]any{"id": "prices", "type": "function", "function": map[string]any{"name": name, "arguments": `{"symbol":"AAPL","start_date":"2025-01-01","end_date":"2099-01-01"}`}}}
				} else if !strings.Contains(req.Messages[len(req.Messages)-1].Content.String(), "to 2025-01-02") {
					t.Error("analysis cutoff missing from tool result")
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if e := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}}); e != nil {
			t.Error(e)
		}
	}))
	defer server.Close()
	t.Setenv("TRADINGAGENTS_LLM_BACKEND_URL", server.URL)
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "offline-test-key")
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.String(), server.URL) {
			return original.RoundTrip(r)
		}
		body := ""
		switch r.URL.Path {
		case "/v1/announcements":
			body = `{"announcements":["Offline announcement"],"require_attention":false}`
		case "/v10/finance/quoteSummary/AAPL":
			body = `{"quoteSummary":{"result":[{"quoteType":{"longName":"Apple Inc."}}]}}`
		case "/v8/finance/chart/AAPL", "/v8/finance/chart/SPY":
			charts.Add(1)
			if r.URL.Query().Get("period1") == "1735689600" && r.URL.Query().Get("period2") != "1735862400" {
				t.Error("future date reached source", r.URL.Query())
			}
			body = `{"chart":{"result":[{"meta":{"exchangeTimezoneName":"UTC"},"timestamp":[1735819200],"indicators":{"quote":[{"open":[100],"high":[102],"low":[99],"close":[101],"volume":[1000]}]}}]}}`
		default:
			return nil, fmt.Errorf("unexpected network request: %s", r.URL.Host+r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	var out bytes.Buffer
	reportDir := filepath.Join(root, "results", "AAPL", "2025-01-02")
	if e = Run(context.Background(), []string{"--checkpoint"}, strings.NewReader("AAPL\n2025-01-02\nmarket\nY\n"+reportDir+"\nN\n"), &out, &out); e != nil {
		t.Fatalf("%v\n%s", e, out.String())
	}
	if calls.Load() != 7 || charts.Load() != 1 {
		t.Fatalf("calls %d charts %d", calls.Load(), charts.Load())
	}
	if !strings.Contains(out.String(), "Signal: Overweight") || !strings.Contains(out.String(), "LLM calls: 7 | Tool calls: 1 | Tokens: 70 input, 35 output") {
		t.Fatal(out.String())
	}
	report := filepath.Join(root, "results", "AAPL", "2025-01-02", "complete_report.md")
	content, e := os.ReadFile(report)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(content), "**Rating**: Overweight") {
		t.Fatal(string(content))
	}
	log := memory.Log{Path: filepath.Join(root, "memory.md")}
	entries, e := log.Entries()
	if e != nil || len(entries) != 1 || entries[0].Rating != "Overweight" {
		t.Fatal(entries, e)
	}
	t.Run("backtest_and_resume", func(t *testing.T) {
		args := []string{"backtest", "AAPL", "--start", "2025-01-02", "--end", "2025-01-02", "--analysts", "market", "--run-id", "smoke"}
		out.Reset()
		if e := Run(context.Background(), args, strings.NewReader(""), &out, &out); e != nil {
			t.Fatalf("%v\n%s", e, out.String())
		}
		if !strings.Contains(out.String(), "Ran 1 cells, skipped 0.") || calls.Load() != 14 {
			t.Fatal(out.String(), calls.Load())
		}
		out.Reset()
		if e := Run(context.Background(), args, strings.NewReader(""), &out, &out); e != nil {
			t.Fatalf("%v\n%s", e, out.String())
		}
		if !strings.Contains(out.String(), "Ran 0 cells, skipped 1.") || calls.Load() != 14 {
			t.Fatal("completed cell replayed", out.String(), calls.Load())
		}
		live, e := log.Entries()
		if e != nil || len(live) != 1 {
			t.Fatal("backtest changed live memory", live, e)
		}
	})
}
