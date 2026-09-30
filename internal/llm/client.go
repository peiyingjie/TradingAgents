package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"tradingagents/internal/callbacks"
	"tradingagents/internal/config"
	"tradingagents/internal/tools"
	"tradingagents/pkg/model"
)

type ChatRequest struct {
	Messages          []model.Message
	Tools             []tools.Spec
	ToolChoice        string
	ResponseFormat    json.RawMessage
	ParallelToolCalls *bool
}
type ChatResponse struct{ Message model.Message }
type Client interface {
	Chat(context.Context, ChatRequest) (*ChatResponse, error)
}
type Provider struct {
	URL, KeyEnv string
	Optional    bool
}

var Providers = map[string]Provider{
	"openai": {"https://api.openai.com/v1", "OPENAI_API_KEY", false}, "xai": {"https://api.x.ai/v1", "XAI_API_KEY", false}, "deepseek": {"https://api.deepseek.com", "DEEPSEEK_API_KEY", false},
	"qwen": {"https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "DASHSCOPE_API_KEY", false}, "qwen-cn": {"https://dashscope.aliyuncs.com/compatible-mode/v1", "DASHSCOPE_CN_API_KEY", false},
	"glm": {"https://api.z.ai/api/paas/v4", "ZHIPU_API_KEY", false}, "glm-cn": {"https://open.bigmodel.cn/api/paas/v4", "ZHIPU_CN_API_KEY", false},
	"minimax": {"https://api.minimax.io/v1", "MINIMAX_API_KEY", false}, "minimax-cn": {"https://api.minimaxi.com/v1", "MINIMAX_CN_API_KEY", false},
	"openrouter": {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", false}, "mistral": {"https://api.mistral.ai/v1", "MISTRAL_API_KEY", false}, "kimi": {"https://api.moonshot.ai/v1", "MOONSHOT_API_KEY", false}, "groq": {"https://api.groq.com/openai/v1", "GROQ_API_KEY", false}, "nvidia": {"https://integrate.api.nvidia.com/v1", "NVIDIA_API_KEY", false},
	"ollama": {"http://localhost:11434/v1", "", true}, "openai_compatible": {"", "OPENAI_COMPATIBLE_API_KEY", true},
	"anthropic": {"https://api.anthropic.com", "ANTHROPIC_API_KEY", false}, "google": {"https://generativelanguage.googleapis.com", "GOOGLE_API_KEY", false}, "azure": {"", "AZURE_OPENAI_API_KEY", false}, "bedrock": {"", "AWS_BEARER_TOKEN_BEDROCK", true},
}

type Adapter struct {
	Provider, Model, BaseURL, APIKey string
	Config                           config.Config
	HTTP                             *http.Client
	Responses                        bool
	Retries                          int
	Region                           string
}

func New(c config.Config, name string) (*Adapter, error) {
	p := strings.ToLower(c.LLMProvider)
	spec, ok := Providers[p]
	if !ok {
		return nil, fmt.Errorf("unsupported LLM provider: %s", p)
	}
	warnUnknownModel(p, name)
	base := c.BackendURL
	if base == "" && p == "ollama" {
		base = os.Getenv("OLLAMA_BASE_URL")
	}
	if base == "" && p == "anthropic" {
		base = os.Getenv("ANTHROPIC_API_URL")
	}
	if base == "" {
		base = spec.URL
	}
	if p == "openai_compatible" && base == "" {
		return nil, fmt.Errorf("provider openai_compatible requires backend_url")
	}
	key := os.Getenv(spec.KeyEnv)
	if p == "google" && key == "" {
		key = os.Getenv("GEMINI_API_KEY")
	}
	if key == "" && !spec.Optional {
		return nil, fmt.Errorf("API key for provider %q is not set; set %s", p, spec.KeyEnv)
	}
	if key == "" && p != "bedrock" {
		key = "EMPTY"
		if p == "ollama" {
			key = "ollama"
		}
	}
	retries := 2
	if p == "google" {
		retries = 6
	}
	if c.LLMMaxRetries != nil {
		retries = *c.LLMMaxRetries
	}
	if retries < 0 {
		return nil, fmt.Errorf("max retries must be non-negative")
	}
	native := false
	if p == "openai" {
		u, err := url.Parse(base)
		if err != nil {
			return nil, err
		}
		native = u.Hostname() == "api.openai.com" || strings.HasSuffix(u.Hostname(), ".openai.com")
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-west-2"
	}
	if p == "azure" {
		deployment := name
		if configured, ok := os.LookupEnv("AZURE_OPENAI_DEPLOYMENT_NAME"); ok {
			deployment = configured
		}
		base = strings.TrimRight(os.Getenv("AZURE_OPENAI_ENDPOINT"), "/") + "/openai/deployments/" + url.PathEscape(deployment)
	}
	if p == "bedrock" && base == "" {
		base = "https://bedrock-runtime." + region + ".amazonaws.com"
	}
	if p == "google" && c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > 2) {
		return nil, fmt.Errorf("temperature must be in [0,2]")
	}
	timeout := 600 * time.Second
	if p == "anthropic" {
		timeout = 0
	} // The retained adapter explicitly disables the SDK timeout.
	return &Adapter{p, name, strings.TrimRight(base, "/"), key, c, &http.Client{Timeout: timeout}, native, retries, region}, nil
}

type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("LLM HTTP %d: %s", e.Status, e.Body) }
func (a *Adapter) Chat(ctx context.Context, r ChatRequest) (result *ChatResponse, resultErr error) {
	callbacks.Notify(ctx, callbacks.Event{Kind: "on_chat_model_start", Name: a.Model, Messages: r.Messages})
	defer func() {
		if resultErr != nil {
			callbacks.Notify(ctx, callbacks.Event{Kind: "on_llm_error", Name: a.Model, Err: resultErr})
		} else if result != nil {
			callbacks.Notify(ctx, callbacks.Event{Kind: "on_llm_end", Name: a.Model, Response: &result.Message})
		}
	}()
	payload, path, err := a.Payload(r)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var response []byte
	for attempt := 0; attempt <= a.Retries; attempt++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", a.BaseURL+path, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		switch a.Provider {
		case "anthropic":
			req.Header.Set("x-api-key", a.APIKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		case "google":
			req.Header.Set("x-goog-api-key", a.APIKey)
		case "azure":
			req.Header.Set("api-key", a.APIKey)
		case "bedrock":
			if a.APIKey != "" {
				req.Header.Set("Authorization", "Bearer "+a.APIKey)
			} else {
				if err = a.signAWS(ctx, req, data); err != nil {
					return nil, err
				}
			}
		default:
			req.Header.Set("Authorization", "Bearer "+a.APIKey)
		}
		resp, e := a.HTTP.Do(req)
		retry := false
		if e != nil {
			err = fmt.Errorf("LLM request: %w", e)
			retry = true
		} else {
			response, e = io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if e != nil {
				err = e
				retry = true
			} else if closeErr != nil {
				err = closeErr
				retry = true
			} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body := string(response)
				if a.APIKey != "" {
					body = strings.ReplaceAll(body, a.APIKey, "[REDACTED]")
				}
				err = &HTTPError{resp.StatusCode, body}
				retry = resp.StatusCode == 408 || resp.StatusCode == 409 || resp.StatusCode == 429 || resp.StatusCode >= 500
			} else {
				err = nil
				break
			}
		}
		if !retry || attempt == a.Retries {
			return nil, err
		}
		delay := time.Duration(1<<min(attempt, 4)) * 500 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return nil, err
	}
	message, err := a.Parse(response)
	if err != nil {
		return nil, fmt.Errorf("parse %s response: %w", a.Provider, err)
	}
	return &ChatResponse{message}, nil
}
func (a *Adapter) StructuredRequest(messages []model.Message, spec tools.Spec) (ChatRequest, error) {
	method := "function_calling"
	if a.Provider == "google" {
		method = "json_schema"
	}
	r := ChatRequest{Messages: messages}
	if method == "json_schema" {
		var err error
		schema := spec.Function.ResponseSchema
		if len(schema) == 0 {
			schema = spec.Function.Parameters
		}
		r.ResponseFormat, err = json.Marshal(map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": spec.Function.Name, "schema": schema}})
		return r, err
	}
	r.Tools = []tools.Spec{spec}
	r.ToolChoice = spec.Function.Name
	m := strings.TrimPrefix(a.Model, "deepseek/")
	if strings.HasPrefix(m, "deepseek-reasoner") || regexp.MustCompile(`^deepseek-v\d`).MatchString(m) || regexp.MustCompile(`^MiniMax-M\d`).MatchString(m) || a.Provider == "ollama" || a.Provider == "openai_compatible" {
		r.ToolChoice = ""
	}
	if a.Provider == "bedrock" {
		switch {
		case strings.Contains(m, "claude") || strings.Contains(m, "nova"):
		case strings.Contains(m, "mistral-large"):
			r.ToolChoice = "any"
		default:
			r.ToolChoice = ""
		}
	}
	if a.Provider != "anthropic" && a.Provider != "google" && a.Provider != "bedrock" {
		v := false
		r.ParallelToolCalls = &v
	}
	return r, nil
}
func StructuredRequest(client Client, msgs []model.Message, spec tools.Spec) (ChatRequest, error) {
	if a, ok := client.(interface {
		StructuredRequest([]model.Message, tools.Spec) (ChatRequest, error)
	}); ok {
		return a.StructuredRequest(msgs, spec)
	}
	return ChatRequest{Messages: msgs, Tools: []tools.Spec{spec}, ToolChoice: spec.Function.Name}, nil
}

// jsonEncoder retains the first error when collecting raw provider fields.
// Callers return err after constructing the complete response.
type jsonEncoder struct{ err error }

func (e *jsonEncoder) encode(v any) json.RawMessage {
	if e.err != nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		e.err = err
	}
	return b
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { s, _ := v.([]any); return s }
func str(v any) string            { s, _ := v.(string); return s }
func num(v any) int               { n, _ := v.(float64); return int(n) }
