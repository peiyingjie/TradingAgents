package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"tradingagents/internal/callbacks"
	"tradingagents/internal/config"
	"tradingagents/internal/tools"
	"tradingagents/pkg/model"
)

func TestRetainedPythonProviderPayloads(t *testing.T) {
	b, e := os.ReadFile("../../tests/fixtures/provider_reference.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Class   string
		Options struct {
			Model           string
			Temperature     *float64
			MaxTokens       *int   `json:"max_tokens"`
			ReasoningEffort string `json:"reasoning_effort"`
			Responses       bool   `json:"use_responses_api"`
			Effort          string
		}
		Payload map[string]any
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	spec := tools.Spec{Type: "function", Function: tools.FunctionSpec{Name: "Pick", Parameters: json.RawMessage(`{"properties":{"action":{"type":"string"}},"required":["action"],"type":"object"}`)}}
	for _, c := range cases {
		t.Run(c.Class+"/"+c.Options.Model+"/"+c.Options.ReasoningEffort, func(t *testing.T) {
			// The reference captures SDK kwargs; extra_body is merged into the
			// top-level JSON object by the OpenAI SDK before HTTP transport.
			if extra, ok := c.Payload["extra_body"].(map[string]any); ok {
				for k, v := range extra {
					c.Payload[k] = v
				}
				delete(c.Payload, "extra_body")
			}
			provider := map[string]string{"NormalizedChatOpenAI": "openai", "DeepSeekChatOpenAI": "deepseek", "MinimaxChatOpenAI": "minimax", "LocalCompatibleChatOpenAI": "openai_compatible", "NormalizedChatAnthropic": "anthropic"}[c.Class]
			a := Adapter{Provider: provider, Model: c.Options.Model, Responses: c.Options.Responses, Config: config.Config{Temperature: c.Options.Temperature, MaxTokens: c.Options.MaxTokens, OpenAIReasoningEffort: c.Options.ReasoningEffort, AnthropicEffort: c.Options.Effort}}
			req, e := a.StructuredRequest([]model.Message{model.NewMessage("system", "system"), model.NewMessage("human", "pick")}, spec)
			if e != nil {
				t.Fatal(e)
			}
			p, _, e := a.Payload(req)
			if e != nil {
				t.Fatal(e)
			}
			raw, e := json.Marshal(p)
			if e != nil {
				t.Fatal(e)
			}
			var actual map[string]any
			if e = json.Unmarshal(raw, &actual); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(actual, c.Payload) {
				want, _ := json.Marshal(c.Payload)
				t.Fatalf("got %s\nwant %s", raw, want)
			}
		})
	}
}
func TestProviderErrorsCancellationAndCallbacks(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(401)
		w.Write([]byte("test-secret denied"))
	}))
	defer server.Close()
	a := Adapter{Provider: "openai_compatible", Model: "test", BaseURL: server.URL, APIKey: "test-secret", HTTP: server.Client(), Retries: 2}
	events := []string{}
	ctx := callbacks.WithHandlers(context.Background(), func(e callbacks.Event) { events = append(events, e.Kind) })
	_, e := a.Chat(ctx, ChatRequest{})
	var httpErr *HTTPError
	if !errors.As(e, &httpErr) || attempts != 1 || strings.Contains(e.Error(), "test-secret") {
		t.Fatal(e, attempts)
	}
	if !reflect.DeepEqual(events, []string{"on_chat_model_start", "on_llm_error"}) {
		t.Fatal(events)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.Chat(cancelled, ChatRequest{}); !errors.Is(e, context.Canceled) || attempts != 1 {
		t.Fatal(e, attempts)
	}
	m := model.NewMessage("ai", "")
	m.ToolCalls = []model.ToolCall{{ID: "bad", Name: "bad", Args: map[string]any{"n": math.NaN()}}}
	if _, e = a.Chat(context.Background(), ChatRequest{Messages: []model.Message{m}}); e == nil || attempts != 1 {
		t.Fatal("invalid arguments sent to server", e)
	}
}
func TestResponsesReasoningReplay(t *testing.T) {
	a := Adapter{Provider: "openai", Model: "gpt-5.6", Responses: true}
	raw := []byte(`{"output":[{"type":"reasoning","id":"r1","encrypted_content":"signed","summary":[]},{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{\"ticker\":\"AAPL\"}"}],"usage":{"input_tokens":2,"output_tokens":3}}`)
	m, e := a.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	result := model.NewMessage("tool", "evidence")
	result.ToolCallID = "c1"
	p, _, e := a.Payload(ChatRequest{Messages: []model.Message{m, result}})
	if e != nil {
		t.Fatal(e)
	}
	input := p["input"].([]any)
	if len(input) != 3 || object(input[0])["encrypted_content"] != "signed" || object(input[2])["call_id"] != "c1" {
		t.Fatal(input)
	}
}

func TestGeminiPythonCheckpointSignedParts(t *testing.T) {
	m := model.NewMessage("ai", "")
	m.Additional = map[string]json.RawMessage{"google_parts": json.RawMessage(`[{"thought_signature":{"__ta_bytes__":"c2lnbmVk"},"function_call":{"name":"lookup","args":{"file_uri":"keep_this_key"}}}]`)}
	a := Adapter{Provider: "google", Model: "gemini-test"}
	p, _, e := a.Payload(ChatRequest{Messages: []model.Message{m}})
	if e != nil {
		t.Fatal(e)
	}
	part := object(array(object(array(p["contents"])[0])["parts"])[0])
	if part["thoughtSignature"] != "c2lnbmVk" || object(object(part["functionCall"])["args"])["file_uri"] != "keep_this_key" {
		t.Fatal(part)
	}
}

func TestAzureDeploymentAndModelAreIndependent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/deployments/production/chat/completions" || r.URL.Query().Get("api-version") != "2025-03-01-preview" {
			t.Error(r.URL)
		}
		if r.Header.Get("api-key") != "test-key" {
			t.Error("missing Azure authentication")
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		if body["model"] != "gpt-4.1" {
			t.Error(body)
		}
		// AzureOpenAIClient's reference passthrough intentionally omits max_tokens.
		if body["max_completion_tokens"] != nil {
			t.Error("Azure token passthrough differs from reference")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	defer server.Close()
	t.Setenv("AZURE_OPENAI_API_KEY", "test-key")
	t.Setenv("AZURE_OPENAI_ENDPOINT", server.URL)
	t.Setenv("AZURE_OPENAI_DEPLOYMENT_NAME", "production")
	t.Setenv("OPENAI_API_VERSION", "2025-03-01-preview")
	tokens := 42
	a, e := New(config.Config{LLMProvider: "azure", MaxTokens: &tokens}, "gpt-4.1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Chat(context.Background(), ChatRequest{Messages: []model.Message{model.NewMessage("human", "hello")}}); e != nil {
		t.Fatal(e)
	}
}
