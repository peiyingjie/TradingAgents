package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"sync"
	"tradingagents/internal/callbacks"
	"tradingagents/internal/state"
	"tradingagents/pkg/model"
)

type FunctionSpec struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Parameters     json.RawMessage `json:"parameters"`
	ResponseSchema json.RawMessage `json:"-"`
}
type Spec struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

//go:embed specs.json
var specsJSON []byte

func Specs() (map[string]Spec, error) {
	var s map[string]Spec
	e := json.Unmarshal(specsJSON, &s)
	return s, e
}

type Tool interface {
	Spec() Spec
	Execute(context.Context, map[string]any) (model.Content, error)
	Injected() map[string]string
}
type Function struct {
	Definition Spec
	Inject     map[string]string
	Run        func(context.Context, map[string]any) (model.Content, error)
}

func (f Function) Spec() Spec                  { return f.Definition }
func (f Function) Injected() map[string]string { return f.Inject }

type ValidationError struct{ Details string }

func (e *ValidationError) Error() string { return e.Details }
func (f Function) Execute(ctx context.Context, args map[string]any) (result model.Content, err error) {
	callbacks.Notify(ctx, callbacks.Event{Kind: "on_tool_start", Name: f.Definition.Function.Name, Args: args})
	defer func() {
		if err != nil {
			callbacks.Notify(ctx, callbacks.Event{Kind: "on_tool_error", Name: f.Definition.Function.Name, Err: err})
		} else {
			callbacks.Notify(ctx, callbacks.Event{Kind: "on_tool_end", Name: f.Definition.Function.Name, Output: result})
		}
	}()
	if e := Validate(f.Definition.Function.Parameters, args); e != nil {
		return model.Content{}, e
	}
	return f.Run(ctx, args)
}

// Validate the public schema before invoking the function; injected state is never advertised.
func Validate(raw json.RawMessage, args map[string]any) error {
	var s struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type  string `json:"type"`
			AnyOf []struct {
				Type string `json:"type"`
			} `json:"anyOf"`
		} `json:"properties"`
	}
	if e := json.Unmarshal(raw, &s); e != nil {
		return e
	}
	for _, k := range s.Required {
		if _, ok := args[k]; !ok {
			return &ValidationError{k + ": Field required"}
		}
	}
	for k, p := range s.Properties {
		v, ok := args[k]
		if !ok {
			continue
		}
		types := []string{p.Type}
		for _, a := range p.AnyOf {
			types = append(types, a.Type)
		}
		valid := false
		for _, typ := range types {
			switch typ {
			case "string":
				_, valid = v.(string)
			case "integer":
				if raw, ok := v.(string); ok {
					parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
					if err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed) && parsed == math.Trunc(parsed) {
						v = parsed
					}
				}
				if raw, ok := v.(bool); ok {
					if raw {
						v = float64(1)
					} else {
						v = float64(0)
					}
				}
				switch n := v.(type) {
				case int:
					valid = true
				case float64:
					valid = n == float64(int64(n))
				}
			case "number":
				if raw, ok := v.(string); ok {
					if parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
						v = parsed
					}
				}
				switch v.(type) {
				case float64, int:
					valid = true
				}
			case "null":
				valid = v == nil
			case "boolean":
				_, valid = v.(bool)
			}
			if valid {
				args[k] = v
				break
			}
		}
		if !valid {
			return &ValidationError{k + ": Input should be a valid " + strings.Join(types, " or ")}
		}
	}
	return nil
}

type Registry struct {
	ByName map[string]Tool
	Names  []string
}

func NewRegistry(items []Tool) (*Registry, error) {
	r := &Registry{ByName: map[string]Tool{}}
	for _, t := range items {
		n := t.Spec().Function.Name
		if _, ok := r.ByName[n]; ok {
			return nil, fmt.Errorf("duplicate tool: %s", n)
		}
		r.ByName[n] = t
		r.Names = append(r.Names, n)
	}
	return r, nil
}

type Executor struct {
	Registry       *Registry
	MaxConcurrency int
}

func (e *Executor) execute(ctx context.Context, call model.ToolCall, s state.State) (model.Message, error) {
	msg := model.Message{Type: "tool", Name: call.Name, ToolCallID: call.ID, Status: "success"}
	item, ok := e.Registry.ByName[call.Name]
	if !ok {
		msg.Status = "error"
		msg.Content = model.Text(fmt.Sprintf("Error: %s is not a valid tool, try one of [%s].", call.Name, strings.Join(e.Registry.Names, ", ")))
		return msg, nil
	}
	args := maps.Clone(call.Args)
	if args == nil {
		args = map[string]any{}
	}
	for key, field := range item.Injected() {
		switch field {
		case "trade_date":
			args[key] = s.TradeDate
		case "company_of_interest":
			args[key] = s.CompanyOfInterest
		default:
			return msg, fmt.Errorf("unknown injected state field: %s", field)
		}
	}
	result, err := item.Execute(ctx, args)
	var validation *ValidationError
	if errors.As(err, &validation) {
		msg.Status = "error"
		msg.Content = model.Text(fmt.Sprintf("Error invoking tool '%s' with kwargs %v with error:\n %s\n Please fix the error and try again.", call.Name, call.Args, validation.Details))
		return msg, nil
	}
	if err != nil {
		return msg, fmt.Errorf("tool %s: %w", call.Name, err)
	}
	msg.Content = result
	return msg, nil
}
func (e *Executor) Invoke(ctx context.Context, s state.State) (state.Update, error) {
	var message *model.Message
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Type == "ai" {
			message = &s.Messages[i]
			break
		}
	}
	if message == nil {
		return state.Update{}, fmt.Errorf("tool execution requires an AIMessage")
	}
	results := make([]model.Message, len(message.ToolCalls))
	errs := make([]error, len(results))
	limit := e.MaxConcurrency
	if limit <= 0 {
		limit = 32
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, call := range message.ToolCalls {
		wg.Add(1)
		go func(i int, c model.ToolCall) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			results[i], errs[i] = e.execute(ctx, c, s)
		}(i, call)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return state.Update{}, err
		}
	}
	return state.Update{Messages: results}, nil
}
