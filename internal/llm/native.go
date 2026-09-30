package llm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"tradingagents/pkg/model"
)

func decodeRaw(raw json.RawMessage) (any, error) {
	var v any
	err := json.Unmarshal(raw, &v)
	return v, err
}

// Python's Gemini SDK persists snake_case fields and byte wrappers. The
// native REST endpoint expects camelCase and base64 strings. Tool arguments
// and response objects are user data and must never have their keys changed.
func googleWire(v any) any {
	if a, ok := v.([]any); ok {
		for i := range a {
			a[i] = googleWire(a[i])
		}
		return a
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if b, ok := m["__ta_bytes__"]; ok && len(m) == 1 {
		return b
	}
	names := map[string]string{"function_call": "functionCall", "function_response": "functionResponse", "thought_signature": "thoughtSignature", "inline_data": "inlineData", "file_data": "fileData", "mime_type": "mimeType", "file_uri": "fileUri", "executable_code": "executableCode", "code_execution_result": "codeExecutionResult", "video_metadata": "videoMetadata", "will_continue": "willContinue"}
	out := map[string]any{}
	for k, value := range m {
		name := k
		if replacement, ok := names[k]; ok {
			name = replacement
		}
		if k != "args" && k != "response" {
			value = googleWire(value)
		}
		out[name] = value
	}
	return out
}
func nativeMessages(messages []model.Message, provider string) ([]any, []any, error) {
	system, history := []any{}, []any{}
	names := map[string]string{}
	for _, m := range messages {
		if m.Type == "system" {
			system = append(system, map[string]any{"text": m.Content.String()})
			continue
		}
		role := "user"
		if m.Type == "ai" {
			role = "assistant"
			if provider == "google" {
				role = "model"
			}
			for _, c := range m.ToolCalls {
				names[c.ID] = c.Name
			}
		}
		blocks := []any{}
		key := "content"
		saved := "anthropic_content"
		if provider == "google" {
			key = "parts"
			saved = "google_parts"
		} else if provider == "bedrock" {
			saved = "bedrock_content"
		}
		if m.Type == "tool" {
			var output any
			parseErr := json.Unmarshal([]byte(m.Content.String()), &output)
			if parseErr != nil {
				output = m.Content.String()
			}
			switch provider {
			case "anthropic":
				blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content, "is_error": m.Status == "error"})
			case "google":
				if _, ok := output.(map[string]any); !ok {
					output = map[string]any{"output": output}
				}
				name := m.Name
				if name == "" {
					name = names[m.ToolCallID]
				}
				blocks = append(blocks, map[string]any{"functionResponse": map[string]any{"name": name, "response": output}})
			case "bedrock":
				v := map[string]any{"json": output}
				if parseErr != nil {
					v = map[string]any{"text": output}
				}
				status := m.Status
				if status == "" {
					status = "success"
				}
				blocks = append(blocks, map[string]any{"toolResult": map[string]any{"toolUseId": m.ToolCallID, "content": []any{v}, "status": status}})
			}
		} else if raw, ok := m.Additional[saved]; m.Type == "ai" && ok {
			v, err := decodeRaw(raw)
			if err != nil {
				return nil, nil, err
			}
			blocks = array(v)
			if provider == "google" {
				blocks = array(googleWire(blocks))
			}
		} else {
			if provider == "anthropic" && m.Content.Blocks != nil {
				for _, raw := range m.Content.Blocks {
					v, err := decodeRaw(raw)
					if err != nil {
						return nil, nil, err
					}
					blocks = append(blocks, v)
				}
			} else if m.Content.String() != "" {
				b := map[string]any{"text": m.Content.String()}
				if provider == "anthropic" {
					b["type"] = "text"
				}
				blocks = append(blocks, b)
			}
			for _, c := range m.ToolCalls {
				switch provider {
				case "anthropic":
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": c.Args})
				case "google":
					blocks = append(blocks, map[string]any{"functionCall": map[string]any{"name": c.Name, "args": c.Args}})
				case "bedrock":
					blocks = append(blocks, map[string]any{"toolUse": map[string]any{"toolUseId": c.ID, "name": c.Name, "input": c.Args}})
				}
			}
		}
		if len(history) > 0 && object(history[len(history)-1])["role"] == role {
			last := object(history[len(history)-1])
			existing := last[key]
			if text, ok := existing.(string); ok {
				existing = []any{map[string]any{"type": "text", "text": text}}
			}
			last[key] = append(array(existing), blocks...)
		} else {
			var content any = blocks
			if provider == "anthropic" && m.Type != "tool" && len(m.ToolCalls) == 0 && m.Additional[saved] == nil && m.Content.Blocks == nil {
				content = m.Content.Text
			}
			history = append(history, map[string]any{"role": role, key: content})
		}
	}
	return system, history, nil
}
func systemText(system []any) string {
	parts := []string{}
	for _, s := range system {
		parts = append(parts, str(object(s)["text"]))
	}
	return strings.Join(parts, "\n\n")
}
func supportsEffort(m string) bool {
	if m == "claude-mythos-preview" || m == "claude-mythos-5" {
		return true
	}
	parts := regexp.MustCompile(`^claude-(opus|sonnet|fable)-(\d+)(?:-(\d+))?$`).FindStringSubmatch(strings.ToLower(m))
	if len(parts) == 0 {
		return false
	}
	major, _ := strconv.Atoi(parts[2])
	minor, _ := strconv.Atoi(parts[3])
	minMajor, minMinor := 4, 5
	if parts[1] == "sonnet" {
		minMinor = 6
	}
	if parts[1] == "fable" {
		minMajor, minMinor = 5, 0
	}
	return major > minMajor || major == minMajor && minor >= minMinor
}
func adaptive(m string) bool {
	for _, p := range []string{"claude-fable-5", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5"} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}
func (a *Adapter) anthropicPayload(r ChatRequest) (map[string]any, string, error) {
	if strings.HasPrefix(a.Model, "claude-fable-5") && a.Config.Temperature != nil && *a.Config.Temperature != 1 {
		return nil, "", fmt.Errorf("temperature is not supported for %s at non-default values", a.Model)
	}
	system, history, err := nativeMessages(r.Messages, "anthropic")
	if err != nil {
		return nil, "", err
	}
	maxTokens := 4096
	if adaptive(a.Model) {
		maxTokens = 128000
	}
	if a.Model == "claude-haiku-4-5" {
		maxTokens = 64000
	}
	if a.Config.MaxTokens != nil {
		maxTokens = *a.Config.MaxTokens
	}
	p := map[string]any{"model": a.Model, "messages": history, "max_tokens": maxTokens}
	if len(system) > 0 {
		p["system"] = systemText(system)
	}
	if a.Config.Temperature != nil && !strings.HasPrefix(a.Model, "claude-fable-5") {
		p["temperature"] = *a.Config.Temperature
	}
	output := map[string]any{}
	if a.Config.AnthropicEffort != "" && supportsEffort(a.Model) {
		output["effort"] = a.Config.AnthropicEffort
		if adaptive(a.Model) {
			p["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		}
	}
	if len(r.Tools) > 0 {
		ts := []any{}
		for _, t := range r.Tools {
			ts = append(ts, map[string]any{"name": t.Function.Name, "description": t.Function.Description, "input_schema": t.Function.Parameters})
		}
		p["tools"] = ts
	}
	if r.ToolChoice != "" {
		switch r.ToolChoice {
		case "auto", "any", "none":
			p["tool_choice"] = map[string]any{"type": r.ToolChoice}
		default:
			p["tool_choice"] = map[string]any{"type": "tool", "name": r.ToolChoice}
		}
	}
	if len(r.ResponseFormat) > 0 {
		var f map[string]any
		if err = json.Unmarshal(r.ResponseFormat, &f); err != nil {
			return nil, "", err
		}
		if f["type"] != "json_schema" {
			return nil, "", fmt.Errorf("Anthropic requires a schema for JSON output")
		}
		output["format"] = map[string]any{"type": "json_schema", "schema": object(f["json_schema"])["schema"]}
	}
	if len(output) > 0 {
		p["output_config"] = output
	}
	return p, "/v1/messages", nil
}
func (a *Adapter) googlePayload(r ChatRequest) (map[string]any, string, error) {
	system, contents, err := nativeMessages(r.Messages, "google")
	if err != nil {
		return nil, "", err
	}
	p := map[string]any{"contents": contents}
	if len(system) > 0 {
		p["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": systemText(system)}}}
	}
	generation := map[string]any{}
	modelName := strings.ToLower(a.Model)
	if a.Config.Temperature != nil && !regexp.MustCompile(`(?:^|/)gemini-(3\.5-flash-lite|3\.6-flash)(-\d{3})?$`).MatchString(modelName) {
		generation["temperature"] = *a.Config.Temperature
	}
	if a.Config.MaxTokens != nil {
		generation["maxOutputTokens"] = *a.Config.MaxTokens
	}
	if level := a.Config.GoogleThinkingLevel; level != "" {
		if level == "minimal" {
			version := regexp.MustCompile(`^gemini-(\d+)\.(\d+)`).FindStringSubmatch(modelName)
			valid := false
			if len(version) > 0 {
				major, _ := strconv.Atoi(version[1])
				minor, _ := strconv.Atoi(version[2])
				valid = !strings.Contains(modelName, "pro") && (major < 3 || major == 3 && minor < 8)
			}
			if !valid {
				level = "low"
			}
		}
		generation["thinkingConfig"] = map[string]any{"thinkingLevel": strings.ToUpper(level)}
	}
	if len(r.Tools) > 0 {
		ts := []any{}
		for _, t := range r.Tools {
			ts = append(ts, map[string]any{"name": t.Function.Name, "description": t.Function.Description, "parametersJsonSchema": t.Function.Parameters})
		}
		p["tools"] = []any{map[string]any{"functionDeclarations": ts}}
	}
	if r.ToolChoice != "" {
		choice := map[string]any{"mode": strings.ToUpper(r.ToolChoice)}
		switch r.ToolChoice {
		case "auto", "any", "none":
		default:
			choice = map[string]any{"mode": "ANY", "allowedFunctionNames": []string{r.ToolChoice}}
		}
		p["toolConfig"] = map[string]any{"functionCallingConfig": choice}
	}
	if len(r.ResponseFormat) > 0 {
		var f map[string]any
		if err = json.Unmarshal(r.ResponseFormat, &f); err != nil {
			return nil, "", err
		}
		generation["responseMimeType"] = "application/json"
		if f["type"] == "json_schema" {
			generation["responseJsonSchema"] = object(f["json_schema"])["schema"]
		}
	}
	if len(generation) > 0 {
		p["generationConfig"] = generation
	}
	return p, "/v1beta/models/" + url.PathEscape(strings.TrimPrefix(a.Model, "models/")) + ":generateContent", nil
}
func (a *Adapter) bedrockPayload(r ChatRequest) (map[string]any, string, error) {
	system, history, err := nativeMessages(r.Messages, "bedrock")
	if err != nil {
		return nil, "", err
	}
	p := map[string]any{"messages": history}
	if len(system) > 0 {
		p["system"] = system
	}
	inf := map[string]any{}
	if a.Config.Temperature != nil {
		inf["temperature"] = *a.Config.Temperature
	}
	if a.Config.MaxTokens != nil {
		inf["maxTokens"] = *a.Config.MaxTokens
	}
	if len(inf) > 0 {
		p["inferenceConfig"] = inf
	}
	if len(r.Tools) > 0 {
		ts := []any{}
		for _, t := range r.Tools {
			ts = append(ts, map[string]any{"toolSpec": map[string]any{"name": t.Function.Name, "description": t.Function.Description, "inputSchema": map[string]any{"json": t.Function.Parameters}}})
		}
		tc := map[string]any{"tools": ts}
		if r.ToolChoice != "" {
			switch r.ToolChoice {
			case "auto", "any":
				tc["toolChoice"] = map[string]any{r.ToolChoice: map[string]any{}}
			default:
				tc["toolChoice"] = map[string]any{"tool": map[string]any{"name": r.ToolChoice}}
			}
		}
		p["toolConfig"] = tc
	}
	if len(r.ResponseFormat) > 0 {
		return nil, "", fmt.Errorf("use function_calling structured output for Bedrock")
	}
	return p, "/model/" + url.PathEscape(a.Model) + "/converse", nil
}
func nativeResult(p map[string]any, blocks []any, key string, provider string) (model.Message, error) {
	m := model.NewMessage("ai", "")
	m.ID = str(p["id"])
	raw, err := json.Marshal(blocks)
	if err != nil {
		return m, err
	}
	m.Additional = map[string]json.RawMessage{key: raw}
	m.Metadata = map[string]json.RawMessage{}
	text := []string{}
	for _, v := range blocks {
		b := object(v)
		switch provider {
		case "anthropic":
			if b["type"] == "text" {
				text = append(text, str(b["text"]))
			}
			if b["type"] == "tool_use" {
				m.ToolCalls = append(m.ToolCalls, model.ToolCall{ID: str(b["id"]), Name: str(b["name"]), Args: object(b["input"]), Type: "tool_call"})
			}
		case "google":
			if str(b["text"]) != "" && b["thought"] != true {
				text = append(text, str(b["text"]))
			}
			c := object(b["functionCall"])
			if c == nil {
				c = object(b["function_call"])
			}
			if c != nil {
				id := str(c["id"])
				if id == "" {
					id = model.NewID()
				}
				m.ToolCalls = append(m.ToolCalls, model.ToolCall{ID: id, Name: str(c["name"]), Args: object(c["args"]), Type: "tool_call"})
			}
		case "bedrock":
			if str(b["text"]) != "" {
				text = append(text, str(b["text"]))
			}
			if c := object(b["toolUse"]); c != nil {
				m.ToolCalls = append(m.ToolCalls, model.ToolCall{ID: str(c["toolUseId"]), Name: str(c["name"]), Args: object(c["input"]), Type: "tool_call"})
			}
		}
	}
	m.Content = model.Text(strings.Join(text, "\n"))
	return m, nil
}
func setUsage(m *model.Message, in, out int) {
	m.Usage = &model.Usage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
}
func parseAnthropic(p map[string]any) (model.Message, error) {
	m, err := nativeResult(p, array(p["content"]), "anthropic_content", "anthropic")
	if err != nil {
		return m, err
	}
	encoder := jsonEncoder{}
	u := object(p["usage"])
	setUsage(&m, num(u["input_tokens"])+num(u["cache_read_input_tokens"])+num(u["cache_creation_input_tokens"]), num(u["output_tokens"]))
	m.Metadata["model_name"] = encoder.encode(p["model"])
	m.Metadata["stop_reason"] = encoder.encode(p["stop_reason"])
	return m, encoder.err
}
func parseGoogle(p map[string]any) (model.Message, error) {
	candidates := array(p["candidates"])
	if len(candidates) == 0 {
		return model.Message{}, fmt.Errorf("Gemini returned no candidates: %v", p["promptFeedback"])
	}
	c := object(candidates[0])
	m, err := nativeResult(p, array(object(c["content"])["parts"]), "google_parts", "google")
	if err != nil {
		return m, err
	}
	encoder := jsonEncoder{}
	m.ID = str(p["responseId"])
	u := object(p["usageMetadata"])
	setUsage(&m, num(u["promptTokenCount"]), num(u["candidatesTokenCount"])+num(u["thoughtsTokenCount"]))
	m.Metadata["finish_reason"] = encoder.encode(c["finishReason"])
	return m, encoder.err
}
func parseBedrock(p map[string]any) (model.Message, error) {
	msg := object(object(p["output"])["message"])
	if msg == nil {
		return model.Message{}, fmt.Errorf("Bedrock returned no output message")
	}
	m, err := nativeResult(p, array(msg["content"]), "bedrock_content", "bedrock")
	if err != nil {
		return m, err
	}
	encoder := jsonEncoder{}
	u := object(p["usage"])
	setUsage(&m, num(u["inputTokens"]), num(u["outputTokens"]))
	m.Metadata["stop_reason"] = encoder.encode(p["stopReason"])
	return m, encoder.err
}
