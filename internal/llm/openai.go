package llm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"tradingagents/pkg/model"
)

func chatMessages(messages []model.Message, deepseek bool) ([]any, error) {
	out := []any{}
	for _, m := range messages {
		item := map[string]any{"role": m.Role(), "content": m.Content}
		if m.Name != "" {
			item["name"] = m.Name
		}
		if m.Type == "tool" {
			item["tool_call_id"] = m.ToolCallID
		}
		if m.Type == "ai" && len(m.ToolCalls) > 0 {
			if m.Content.String() == "" {
				item["content"] = nil
			}
			calls := []any{}
			for _, c := range m.ToolCalls {
				args, err := json.Marshal(c.Args)
				if err != nil {
					return nil, fmt.Errorf("tool call %s arguments: %w", c.ID, err)
				}
				calls = append(calls, map[string]any{"type": "function", "id": c.ID, "function": map[string]any{"name": c.Name, "arguments": string(args)}})
			}
			item["tool_calls"] = calls
		}
		if deepseek {
			if v, ok := m.Additional["reasoning_content"]; ok {
				item["reasoning_content"] = v
			}
		}
		out = append(out, item)
	}
	return out, nil
}
func (a *Adapter) Payload(r ChatRequest) (map[string]any, string, error) {
	switch a.Provider {
	case "anthropic":
		return a.anthropicPayload(r)
	case "google":
		return a.googlePayload(r)
	case "bedrock":
		return a.bedrockPayload(r)
	}
	messages, err := chatMessages(r.Messages, a.Provider == "deepseek")
	if err != nil {
		return nil, "", err
	}
	p := map[string]any{"model": a.Model, "messages": messages}
	if a.Config.Temperature != nil {
		p["temperature"] = *a.Config.Temperature
	}
	if a.Config.MaxTokens != nil && a.Provider != "azure" {
		p["max_completion_tokens"] = *a.Config.MaxTokens
	}
	effort := a.Config.OpenAIReasoningEffort
	if a.Provider != "openai" {
		effort = ""
	}
	if effort != "" && regexp.MustCompile(`^(?:gpt-(?:[5-9]|[1-9]\d)|o[1-9])(?:[.-]|$)`).MatchString(strings.ToLower(a.Model)) {
		p["reasoning_effort"] = effort
	}
	if strings.HasPrefix(a.Model, "o1") && a.Config.Temperature == nil {
		p["temperature"] = 1
	} else if strings.HasPrefix(a.Model, "gpt-5") && !strings.Contains(a.Model, "chat") && effort != "none" && (a.Config.Temperature == nil || *a.Config.Temperature != 1) {
		delete(p, "temperature")
	}
	if regexp.MustCompile(`^o\d`).MatchString(a.Model) {
		for _, m := range p["messages"].([]any) {
			if object(m)["role"] == "system" {
				object(m)["role"] = "developer"
			}
		}
	}
	if len(r.Tools) > 0 {
		p["tools"] = r.Tools
	}
	if r.ParallelToolCalls != nil {
		p["parallel_tool_calls"] = *r.ParallelToolCalls
	}
	if r.ToolChoice != "" {
		switch r.ToolChoice {
		case "auto", "none", "required":
			p["tool_choice"] = r.ToolChoice
		default:
			p["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": r.ToolChoice}}
		}
	}
	if len(r.ResponseFormat) > 0 {
		p["response_format"] = r.ResponseFormat
	}
	if strings.HasPrefix(a.Provider, "minimax") && regexp.MustCompile(`^MiniMax-M\d`).MatchString(a.Model) {
		p["reasoning_split"] = true
	}
	path := "/chat/completions"
	if a.Provider == "azure" {
		path += "?api-version=" + url.QueryEscape(os.Getenv("OPENAI_API_VERSION"))
	}
	if !a.Responses {
		return p, path, nil
	}
	delete(p, "messages")
	items := []any{}
	for _, m := range r.Messages {
		if raw, ok := m.Additional["responses_output"]; m.Type == "ai" && ok {
			var prior []any
			if err := json.Unmarshal(raw, &prior); err != nil {
				return nil, "", err
			}
			items = append(items, prior...)
		} else if m.Type == "tool" {
			items = append(items, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		} else if m.Type == "ai" {
			if m.Content.String() != "" {
				items = append(items, map[string]any{"type": "message", "role": "assistant", "content": m.Content})
			}
			for _, c := range m.ToolCalls {
				args, err := json.Marshal(c.Args)
				if err != nil {
					return nil, "", err
				}
				items = append(items, map[string]any{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": string(args)})
			}
		} else {
			encoded, err := chatMessages([]model.Message{m}, false)
			if err != nil {
				return nil, "", err
			}
			v := object(encoded[0])
			v["type"] = "message"
			items = append(items, v)
		}
	}
	p["input"] = items
	if v, ok := p["max_completion_tokens"]; ok {
		p["max_output_tokens"] = v
		delete(p, "max_completion_tokens")
	}
	if v, ok := p["reasoning_effort"]; ok {
		p["reasoning"] = map[string]any{"effort": v}
		delete(p, "reasoning_effort")
	}
	if strings.HasPrefix(a.Model, "gpt-5") && !strings.Contains(a.Model, "chat") && effort != "none" {
		delete(p, "temperature")
	}
	if len(r.Tools) > 0 {
		ts := []any{}
		for _, t := range r.Tools {
			ts = append(ts, map[string]any{"type": "function", "name": t.Function.Name, "description": t.Function.Description, "parameters": t.Function.Parameters})
		}
		p["tools"] = ts
	}
	if v, ok := p["tool_choice"].(map[string]any); ok {
		p["tool_choice"] = map[string]any{"type": "function", "name": object(v["function"])["name"]}
	}
	if len(r.ResponseFormat) > 0 {
		var f map[string]any
		if err := json.Unmarshal(r.ResponseFormat, &f); err != nil {
			return nil, "", err
		}
		if f["type"] == "json_schema" {
			f = object(f["json_schema"])
			f["type"] = "json_schema"
		}
		p["text"] = map[string]any{"format": f}
		delete(p, "response_format")
	}
	return p, "/responses", nil
}
func parseCalls(m *model.Message, calls []any) {
	for _, v := range calls {
		c := object(v)
		f := object(c["function"])
		args := map[string]any{}
		err := json.Unmarshal([]byte(str(f["arguments"])), &args)
		if err == nil && args != nil {
			m.ToolCalls = append(m.ToolCalls, model.ToolCall{ID: str(c["id"]), Name: str(f["name"]), Args: args, Type: "tool_call"})
		} else {
			detail := "Tool arguments must be a JSON object"
			if err != nil {
				detail = err.Error()
			}
			m.InvalidToolCalls = append(m.InvalidToolCalls, model.InvalidToolCall{ID: str(c["id"]), Name: str(f["name"]), Args: str(f["arguments"]), Error: detail})
		}
	}
}
func (a *Adapter) Parse(data []byte) (model.Message, error) {
	var p map[string]any
	if err := json.Unmarshal(data, &p); err != nil {
		return model.Message{}, err
	}
	if p["error"] != nil {
		return model.Message{}, fmt.Errorf("provider error: %v", p["error"])
	}
	switch a.Provider {
	case "anthropic":
		return parseAnthropic(p)
	case "google":
		return parseGoogle(p)
	case "bedrock":
		return parseBedrock(p)
	}
	m := model.NewMessage("ai", "")
	m.ID = str(p["id"])
	m.Additional = map[string]json.RawMessage{}
	encoder := jsonEncoder{}
	m.Metadata = map[string]json.RawMessage{"model_name": encoder.encode(p["model"])}
	u := object(p["usage"])
	if !a.Responses {
		choices := array(p["choices"])
		if len(choices) == 0 {
			return m, fmt.Errorf("provider returned no completion choices")
		}
		choice := object(choices[0])
		msg := object(choice["message"])
		content := encoder.encode(msg["content"])
		if encoder.err != nil {
			return m, encoder.err
		}
		if err := json.Unmarshal(content, &m.Content); err != nil {
			return m, err
		}
		parseCalls(&m, array(msg["tool_calls"]))
		if a.Provider == "deepseek" && msg["reasoning_content"] != nil {
			m.Additional["reasoning_content"] = encoder.encode(msg["reasoning_content"])
		}
		m.Metadata["finish_reason"] = encoder.encode(choice["finish_reason"])
		m.Usage = &model.Usage{InputTokens: num(u["prompt_tokens"]), OutputTokens: num(u["completion_tokens"])}
	} else {
		texts := []string{}
		calls := []any{}
		for _, v := range array(p["output"]) {
			item := object(v)
			switch item["type"] {
			case "message":
				for _, b := range array(item["content"]) {
					block := object(b)
					if block["type"] == "output_text" {
						texts = append(texts, str(block["text"]))
					}
				}
			case "function_call":
				calls = append(calls, map[string]any{"id": item["call_id"], "function": map[string]any{"name": item["name"], "arguments": item["arguments"]}})
			}
		}
		m.Content = model.Text(strings.Join(texts, "\n"))
		parseCalls(&m, calls)
		m.Additional["responses_output"] = encoder.encode(p["output"])
		m.Metadata["status"] = encoder.encode(p["status"])
		m.Usage = &model.Usage{InputTokens: num(u["input_tokens"]), OutputTokens: num(u["output_tokens"])}
	}
	m.Usage.TotalTokens = m.Usage.InputTokens + m.Usage.OutputTokens
	return m, encoder.err
}
