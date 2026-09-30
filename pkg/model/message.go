package model

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// Content preserves provider text and multimodal block lists without weakening state types.
type Content struct {
	Text   string
	Blocks []json.RawMessage
}

func Text(s string) Content { return Content{Text: s} }
func (c Content) MarshalJSON() ([]byte, error) {
	if c.Blocks != nil {
		return json.Marshal(c.Blocks)
	}
	return json.Marshal(c.Text)
}
func (c *Content) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		c.Text = ""
		return json.Unmarshal(b, &c.Blocks)
	}
	c.Blocks = nil
	return json.Unmarshal(b, &c.Text)
}
func (c Content) String() string {
	if c.Blocks == nil {
		return c.Text
	}
	var s []string
	for _, b := range c.Blocks {
		var plain string
		if json.Unmarshal(b, &plain) == nil {
			if plain != "" {
				s = append(s, plain)
			}
			continue
		}
		var t struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(b, &t) == nil && t.Type == "text" && t.Text != "" {
			s = append(s, t.Text)
		}
	}
	return strings.Join(s, "\n")
}

type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
	Type string         `json:"type,omitempty"`
}
type InvalidToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Args  string `json:"args"`
	Error string `json:"error"`
}
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
type Message struct {
	Type             string                     `json:"type"`
	Content          Content                    `json:"content"`
	ID               string                     `json:"id,omitempty"`
	Name             string                     `json:"name,omitempty"`
	ToolCallID       string                     `json:"tool_call_id,omitempty"`
	Status           string                     `json:"status,omitempty"`
	ToolCalls        []ToolCall                 `json:"tool_calls,omitempty"`
	InvalidToolCalls []InvalidToolCall          `json:"invalid_tool_calls,omitempty"`
	Additional       map[string]json.RawMessage `json:"additional_kwargs,omitempty"`
	Metadata         map[string]json.RawMessage `json:"response_metadata,omitempty"`
	Usage            *Usage                     `json:"usage_metadata,omitempty"`
}

func NewMessage(role, content string) Message {
	switch role {
	case "user":
		role = "human"
	case "assistant":
		role = "ai"
	}
	return Message{Type: role, Content: Text(content)}
}
func NewID() string { return rand.Text() }
func (m Message) Role() string {
	switch m.Type {
	case "human":
		return "user"
	case "ai":
		return "assistant"
	}
	return m.Type
}
func AddMessages(left, right []Message) ([]Message, error) {
	result := append([]Message{}, left...)
	incoming := append([]Message{}, right...)
	for i := range result {
		if result[i].ID == "" {
			result[i].ID = NewID()
		}
	}
	for i := range incoming {
		if incoming[i].ID == "" {
			incoming[i].ID = NewID()
		}
	}
	positions := map[string]int{}
	removed := map[string]bool{}
	for i, m := range result {
		positions[m.ID] = i
	}
	for _, m := range incoming {
		i, exists := positions[m.ID]
		if m.Type == "remove" {
			if !exists {
				return nil, fmt.Errorf("cannot remove unknown message ID: %s", m.ID)
			}
			removed[m.ID] = true
		} else if exists {
			result[i] = m
			delete(removed, m.ID)
		} else {
			positions[m.ID] = len(result)
			result = append(result, m)
		}
	}
	out := make([]Message, 0, len(result))
	for _, m := range result {
		if !removed[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}
