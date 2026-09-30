package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"tradingagents/internal/config"
	"tradingagents/pkg/model"
)

// Every factory path is exercised with disposable credentials and loopback HTTP.
func TestSmokeAllRegisteredProviders(t *testing.T) {
	for provider, spec := range Providers {
		t.Run(provider, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				response := `{"choices":[{"message":{"content":"smoke-ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
				header, want, path := "Authorization", "Bearer smoke-key", "/chat/completions"
				switch provider {
				case "anthropic":
					header, want, path = "x-api-key", "smoke-key", "/v1/messages"
					response = `{"content":[{"type":"text","text":"smoke-ok"}],"usage":{"input_tokens":1,"output_tokens":2}}`
				case "google":
					header, want, path = "x-goog-api-key", "smoke-key", "/v1beta/models/smoke-model:generateContent"
					response = `{"candidates":[{"content":{"parts":[{"text":"smoke-ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2}}`
				case "azure":
					header, want, path = "api-key", "smoke-key", "/openai/deployments/smoke-deployment/chat/completions"
				case "bedrock":
					path = "/model/smoke-model/converse"
					response = `{"output":{"message":{"content":[{"text":"smoke-ok"}]}},"usage":{"inputTokens":1,"outputTokens":2}}`
				case "ollama":
					want = "Bearer ollama"
				}
				if r.URL.Path != path || r.Header.Get(header) != want {
					t.Errorf("wrong provider request path/auth: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(response))
			}))
			defer server.Close()
			if spec.KeyEnv != "" {
				t.Setenv(spec.KeyEnv, "smoke-key")
			}
			t.Setenv("AZURE_OPENAI_ENDPOINT", server.URL)
			t.Setenv("AZURE_OPENAI_DEPLOYMENT_NAME", "smoke-deployment")
			t.Setenv("OPENAI_API_VERSION", "2025-03-01-preview")
			zero := 0
			a, err := New(config.Config{LLMProvider: provider, BackendURL: server.URL, LLMMaxRetries: &zero}, "smoke-model")
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.Chat(context.Background(), ChatRequest{Messages: []model.Message{model.NewMessage("human", "smoke")}})
			if err != nil {
				t.Fatal(err)
			}
			if requests != 1 || result.Message.Content.String() != "smoke-ok" || result.Message.Usage.TotalTokens != 3 {
				t.Fatal(requests, result)
			}
		})
	}
}

func TestSmokeNativeOpenAIResponsesAndAWSSigning(t *testing.T) {
	for _, provider := range []string{"openai", "bedrock"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if provider == "openai" {
					if r.URL.Path != "/responses" {
						t.Error(r.URL.Path)
					}
					w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"smoke-ok"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`))
				} else {
					if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || r.Header.Get("X-Amz-Security-Token") != "smoke-session" {
						t.Error("SigV4 authentication missing")
					}
					w.Write([]byte(`{"output":{"message":{"content":[{"text":"smoke-ok"}]}}}`))
				}
			}))
			defer server.Close()
			t.Setenv("OPENAI_API_KEY", "smoke-key")
			t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
			t.Setenv("AWS_ACCESS_KEY_ID", "smoke-access")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "smoke-secret")
			t.Setenv("AWS_SESSION_TOKEN", "smoke-session")
			t.Setenv("AWS_PROFILE", "")
			t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/config")
			t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/credentials")
			t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
			a, err := New(config.Config{LLMProvider: provider}, "smoke-model")
			if err != nil {
				t.Fatal(err)
			}
			if provider == "openai" && !a.Responses {
				t.Fatal("official endpoint did not choose Responses")
			}
			a.BaseURL = server.URL
			result, err := a.Chat(context.Background(), ChatRequest{Messages: []model.Message{model.NewMessage("human", "smoke")}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Message.Content.String() != "smoke-ok" {
				t.Fatal(result)
			}
		})
	}
}
