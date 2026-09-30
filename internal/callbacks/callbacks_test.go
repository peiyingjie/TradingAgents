package callbacks

import (
	"context"
	"sync"
	"testing"
)

func TestInheritedObserversSerializeAndRecover(t *testing.T) {
	count := 0
	ctx := WithHandlers(context.Background(), func(Event) { count++ })
	ctx = WithHandlers(ctx, func(Event) { panic("observer error") })
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); Notify(ctx, Event{Kind: "on_tool_start"}) }()
	}
	wg.Wait()
	if count != 8 {
		t.Fatal(count)
	}
}
