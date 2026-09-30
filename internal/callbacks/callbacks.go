// Package callbacks carries optional lifecycle observers through a run's context.
package callbacks

import (
	"context"
	"log/slog"
	"sync"
	"tradingagents/pkg/model"
)

type Event struct {
	Kind     string
	Name     string
	Messages []model.Message
	Response *model.Message
	Args     map[string]any
	Output   model.Content
	Err      error
}
type Handler func(Event)
type contextKey struct{}
type observer struct {
	mu      sync.Mutex
	handler Handler
}

func WithHandlers(ctx context.Context, handlers ...Handler) context.Context {
	inherited, _ := ctx.Value(contextKey{}).([]*observer)
	all := append([]*observer(nil), inherited...)
	for _, h := range handlers {
		if h != nil {
			all = append(all, &observer{handler: h})
		}
	}
	return context.WithValue(ctx, contextKey{}, all)
}
func Notify(ctx context.Context, event Event) {
	all, _ := ctx.Value(contextKey{}).([]*observer)
	for _, o := range all {
		func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			defer func() {
				if p := recover(); p != nil {
					slog.Warn("callback failed", "event", event.Kind, "error", p)
				}
			}()
			o.handler(event)
		}()
	}
}
