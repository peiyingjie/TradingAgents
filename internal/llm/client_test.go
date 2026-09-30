package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"tradingagents/internal/config"
	"tradingagents/internal/tools"
	"tradingagents/pkg/model"
)

func TestProviderHTTPRoundTrips(t *testing.T) {
	for _, p := range []string{"openai_compatible", "anthropic", "google", "bedrock"} {
		t.Run(p, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Error(e)
				}
				w.Header().Set("Content-Type", "application/json")
				switch p {
				case "anthropic":
					if body["system"] != "system" {
						t.Error(body)
					}
					w.Write([]byte(`{"id":"a","content":[{"type":"thinking","thinking":"hidden","signature":"signed"},{"type":"text","text":"answer"},{"type":"tool_use","id":"c","name":"echo","input":{"x":1}}],"usage":{"input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}`))
				case "google":
					if body["systemInstruction"] == nil {
						t.Error(body)
					}
					w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"signed"},{"functionCall":{"id":"c","name":"echo","args":{"x":1}}}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"thoughtsTokenCount":1}}`))
				case "bedrock":
					w.Write([]byte(`{"output":{"message":{"content":[{"text":"answer"},{"toolUse":{"toolUseId":"c","name":"echo","input":{"x":1}}}]}},"usage":{"inputTokens":2,"outputTokens":4}}`))
				default:
					w.Write([]byte(`{"choices":[{"message":{"content":"answer","tool_calls":[{"id":"c","function":{"name":"echo","arguments":"{\"x\":1}"}}]}}],"usage":{"prompt_tokens":2,"completion_tokens":4}}`))
				}
			}))
			defer server.Close()
			a := Adapter{Provider: p, Model: "test", BaseURL: server.URL, APIKey: "test", HTTP: server.Client(), Config: config.Config{}}
			result, e := a.Chat(context.Background(), ChatRequest{Messages: []model.Message{model.NewMessage("system", "system"), model.NewMessage("user", "question")}})
			if e != nil {
				t.Fatal(e)
			}
			if result.Message.Content.String() != "answer" || len(result.Message.ToolCalls) != 1 || result.Message.Usage.OutputTokens != 4 {
				t.Fatal(result)
			}
		})
	}
}
func TestReasoningAndStructuredCapabilities(t *testing.T) {
	spec := tools.Spec{Type: "function", Function: tools.FunctionSpec{Name: "Decision", Parameters: json.RawMessage(`{"type":"object"}`)}}
	for _, name := range []string{"deepseek-v4-flash", "deepseek/deepseek-reasoner", "MiniMax-M2.7"} {
		a := Adapter{Provider: "openrouter", Model: name}
		r, err := a.StructuredRequest(nil, spec)
		if err != nil {
			t.Fatal(err)
		}
		if r.ToolChoice != "" || len(r.Tools) != 1 {
			t.Fatal(r)
		}
	}
	a := Adapter{Provider: "deepseek", Model: "deepseek-reasoner"}
	m := model.NewMessage("assistant", "")
	m.Additional = map[string]json.RawMessage{"reasoning_content": json.RawMessage(`"reason"`)}
	p, _, e := a.Payload(ChatRequest{Messages: []model.Message{m}})
	if e != nil {
		t.Fatal(e)
	}
	if object(p["messages"].([]any)[0])["reasoning_content"] == nil {
		t.Fatal(p)
	}
}
