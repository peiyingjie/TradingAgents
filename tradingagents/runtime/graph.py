"""A single active node and an explicit router: the graph this project needs."""

from __future__ import annotations

import inspect
from copy import deepcopy
from typing import Annotated, get_args, get_origin, get_type_hints

from typing_extensions import TypedDict

from .callbacks import run_config
from .messages import add_messages

START, END = "__start__", "__end__"


class MessagesState(TypedDict):
    messages: Annotated[list, add_messages]


class GraphRecursionError(RuntimeError):
    pass


class StateGraph:
    def __init__(self, state_schema):
        self.state_schema = state_schema
        self.nodes = {}
        self.edges = {}
        self.routes = {}

    def add_node(self, name, node):
        if name in self.nodes or name in (START, END):
            raise ValueError(f"Duplicate or reserved node: {name}")
        self.nodes[name] = node
        return self

    def add_edge(self, source, target):
        if source in self.edges or source in self.routes:
            raise ValueError(f"Node {source} already has an outgoing edge")
        self.edges[source] = target
        return self

    def set_entry_point(self, name):
        return self.add_edge(START, name)

    def add_conditional_edges(self, source, router, path_map):
        if source in self.edges or source in self.routes:
            raise ValueError(f"Node {source} already has an outgoing edge")
        paths = path_map if isinstance(path_map, dict) else {p: p for p in path_map}
        self.routes[source] = (router, dict(paths))
        return self

    def compile(self, checkpointer=None):
        if START not in self.edges:
            raise ValueError("Graph requires a START edge")
        for source in (*self.edges, *self.routes):
            if source != START and source not in self.nodes:
                raise ValueError(f"Unknown source node: {source}")
        targets = [
            *self.edges.values(),
            *(t for _, paths in self.routes.values() for t in paths.values()),
        ]
        for target in targets:
            if target != END and target not in self.nodes:
                raise ValueError(f"Unknown target node: {target}")
        for node in self.nodes:
            if node not in self.edges and node not in self.routes:
                raise ValueError(f"Missing outgoing edge: {node}")
        return GraphExecutor(self, checkpointer)


class GraphExecutor:
    def __init__(self, graph, checkpointer):
        self.nodes = dict(graph.nodes)
        self.edges = dict(graph.edges)
        self.routes = dict(graph.routes)
        self.checkpointer = checkpointer
        self.fields = get_type_hints(graph.state_schema, include_extras=True)
        self.reducers = {}
        for key, annotation in self.fields.items():
            if get_origin(annotation) is Annotated:
                reducers = [a for a in get_args(annotation)[1:] if callable(a)]
                if reducers:
                    self.reducers[key] = reducers[-1]

    def _merge(self, state, update):
        if not isinstance(update, dict):
            raise TypeError("Graph nodes must return a state update dict")
        result = dict(state)
        for key, value in update.items():
            if key in self.fields:
                result[key] = (
                    self.reducers[key](result.get(key), value) if key in self.reducers else value
                )
        return result

    def stream(self, input, config=None, *, stream_mode="updates"):
        if stream_mode not in ("values", "updates"):
            raise ValueError(f"Unsupported stream mode: {stream_mode}")
        config = dict(config or {})
        limit = config.get("recursion_limit", 25)
        if not isinstance(limit, int) or limit < 1:
            raise ValueError("recursion_limit must be a positive integer")
        tid = config.get("configurable", {}).get("thread_id")
        if self.checkpointer is not None and not tid:
            raise ValueError("Checkpoint execution requires configurable.thread_id")
        saved = self.checkpointer.load(tid) if self.checkpointer is not None else None
        if input is None:
            if saved is None:
                raise ValueError("No checkpoint to resume")
            state, node, step = saved.state, saved.next_node, saved.step
        else:
            state = (
                saved.state
                if saved
                else {k: reducer(None, []) for k, reducer in self.reducers.items()}
            )
            state = self._merge(deepcopy(state), deepcopy(input))
            node, step = self.edges[START], 0
            if self.checkpointer is not None:
                self.checkpointer.save(tid, state, node, step)
        if stream_mode == "values":
            yield deepcopy(state)
        for _ in range(limit):
            if node == END:
                return
            token = run_config.set(config)
            try:
                action = self.nodes[node]
                function = action.invoke if hasattr(action, "invoke") else action
                kwargs = (
                    {"config": config} if "config" in inspect.signature(function).parameters else {}
                )
                update = function(deepcopy(state), **kwargs)
                merged = self._merge(state, update)
                if node in self.routes:
                    router, paths = self.routes[node]
                    route = router(deepcopy(merged))
                    if route not in paths:
                        raise ValueError(f"Router for {node} returned unmapped target: {route!r}")
                    next_node = paths[route]
                else:
                    next_node = self.edges[node]
                step += 1
                if self.checkpointer is not None:
                    self.checkpointer.save(tid, merged, next_node, step)
                state = merged
            finally:
                run_config.reset(token)
            completed, node = node, next_node
            yield deepcopy(state if stream_mode == "values" else {completed: update})
        if node != END:
            raise GraphRecursionError(f"Recursion limit of {limit} reached without reaching END")

    def invoke(self, input, config=None, *, stream_mode="values"):
        final = None
        for state in self.stream(input, config, stream_mode="values"):
            final = state
        return final
