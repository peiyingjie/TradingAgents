// Package runtime preserves the reference's single active node execution model.
package runtime

import (
	"context"
	"fmt"
	"maps"
	"tradingagents/internal/state"
)

const (
	Start = "__start__"
	End   = "__end__"
)

type Node func(context.Context, state.State) (state.Update, error)
type Router func(context.Context, state.State) (string, error)
type Route struct {
	Router Router
	Paths  map[string]string
}
type Graph struct {
	Nodes  map[string]Node
	Edges  map[string]string
	Routes map[string]Route
}

func NewGraph() *Graph { return &Graph{map[string]Node{}, map[string]string{}, map[string]Route{}} }
func (g *Graph) AddNode(name string, node Node) error {
	if _, ok := g.Nodes[name]; ok || name == Start || name == End {
		return fmt.Errorf("duplicate or reserved node: %s", name)
	}
	if node == nil {
		return fmt.Errorf("nil node: %s", name)
	}
	g.Nodes[name] = node
	return nil
}
func (g *Graph) checkEdge(source string) error {
	_, a := g.Edges[source]
	_, b := g.Routes[source]
	if a || b {
		return fmt.Errorf("node %s already has an outgoing edge", source)
	}
	return nil
}
func (g *Graph) AddEdge(source, target string) error {
	if e := g.checkEdge(source); e != nil {
		return e
	}
	g.Edges[source] = target
	return nil
}
func (g *Graph) AddRoute(source string, router Router, paths map[string]string) error {
	if e := g.checkEdge(source); e != nil {
		return e
	}
	if router == nil {
		return fmt.Errorf("nil router: %s", source)
	}
	g.Routes[source] = Route{router, maps.Clone(paths)}
	return nil
}
func (g *Graph) Compile(store CheckpointStore) (*Executor, error) {
	if _, ok := g.Edges[Start]; !ok {
		return nil, fmt.Errorf("graph requires a START edge")
	}
	check := func(source, target string) error {
		if source != Start {
			if _, ok := g.Nodes[source]; !ok {
				return fmt.Errorf("unknown source node: %s", source)
			}
		}
		if target != End {
			if _, ok := g.Nodes[target]; !ok {
				return fmt.Errorf("unknown target node: %s", target)
			}
		}
		return nil
	}
	for s, t := range g.Edges {
		if e := check(s, t); e != nil {
			return nil, e
		}
	}
	routes := map[string]Route{}
	for s, r := range g.Routes {
		for _, t := range r.Paths {
			if e := check(s, t); e != nil {
				return nil, e
			}
		}
		routes[s] = Route{r.Router, maps.Clone(r.Paths)}
	}
	for n := range g.Nodes {
		_, a := g.Edges[n]
		_, b := g.Routes[n]
		if !a && !b {
			return nil, fmt.Errorf("missing outgoing edge: %s", n)
		}
	}
	return &Executor{maps.Clone(g.Nodes), maps.Clone(g.Edges), routes, store}, nil
}

type RecursionError struct{ Limit int }

func (e *RecursionError) Error() string {
	return fmt.Sprintf("recursion limit of %d reached without reaching END", e.Limit)
}

type Options struct {
	RecursionLimit int
	ThreadID       string
	StreamMode     string
	OnEvent        func(Event) error
}
type Event struct {
	Node   string
	Next   string
	Step   int
	State  state.State
	Update state.Update
}
type Executor struct {
	nodes  map[string]Node
	edges  map[string]string
	routes map[string]Route
	store  CheckpointStore
}

func (e *Executor) Invoke(ctx context.Context, input *state.Update, opts Options) (state.State, error) {
	var s state.State
	limit := opts.RecursionLimit
	if limit == 0 {
		limit = 25
	}
	if limit < 1 {
		return s, fmt.Errorf("recursion_limit must be a positive integer")
	}
	mode := opts.StreamMode
	if mode == "" {
		mode = "values"
	}
	if mode != "values" && mode != "updates" {
		return s, fmt.Errorf("unsupported stream mode: %s", mode)
	}
	var saved *Checkpoint
	var err error
	if e.store != nil {
		if opts.ThreadID == "" {
			return s, fmt.Errorf("checkpoint execution requires thread_id")
		}
		saved, err = e.store.Load(ctx, opts.ThreadID)
		if err != nil {
			return s, err
		}
	}
	node, step := e.edges[Start], 0
	if input == nil {
		if saved == nil {
			return s, fmt.Errorf("no checkpoint to resume")
		}
		s, node, step = saved.State, saved.NextNode, saved.Step
	} else {
		if saved != nil {
			s = saved.State
		}
		s, err = state.Merge(s, *input)
		if err != nil {
			return s, err
		}
		s, err = state.Clone(s)
		if err != nil {
			return s, err
		}
		if e.store != nil {
			if err = e.store.Save(ctx, opts.ThreadID, Checkpoint{s, node, step}); err != nil {
				return s, err
			}
		}
	}
	emit := func(name, next string, u state.Update) error {
		if opts.OnEvent == nil {
			return nil
		}
		snapshot, err := state.Clone(s)
		if err != nil {
			return err
		}
		return opts.OnEvent(Event{name, next, step, snapshot, u})
	}
	if mode == "values" {
		if err = emit("", node, state.Update{}); err != nil {
			return s, err
		}
	}
	for i := 0; i < limit; i++ {
		if err = ctx.Err(); err != nil {
			return s, err
		}
		if node == End {
			return s, nil
		}
		action, ok := e.nodes[node]
		if !ok {
			return s, fmt.Errorf("unknown checkpoint node: %s", node)
		}
		snapshot, err := state.Clone(s)
		if err != nil {
			return s, err
		}
		u, err := action(ctx, snapshot)
		if err != nil {
			return s, fmt.Errorf("node %s: %w", node, err)
		}
		merged, err := state.Merge(s, u)
		if err != nil {
			return s, fmt.Errorf("merge %s: %w", node, err)
		}
		next := e.edges[node]
		if route, ok := e.routes[node]; ok {
			copyState, err := state.Clone(merged)
			if err != nil {
				return s, err
			}
			key, err := route.Router(ctx, copyState)
			if err != nil {
				return s, fmt.Errorf("route %s: %w", node, err)
			}
			next, ok = route.Paths[key]
			if !ok {
				return s, fmt.Errorf("router for %s returned unmapped target: %q", node, key)
			}
		}
		if e.store != nil {
			if err = e.store.Save(ctx, opts.ThreadID, Checkpoint{merged, next, step + 1}); err != nil {
				return s, err
			}
		}
		s = merged
		step++
		completed := node
		node = next
		if err = emit(completed, next, u); err != nil {
			return s, err
		}
	}
	if node != End {
		return s, &RecursionError{limit}
	}
	return s, nil
}
