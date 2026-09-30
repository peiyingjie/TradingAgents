package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
	"tradingagents/internal/graph"
	"tradingagents/internal/llm"
	"tradingagents/internal/state"
)

func TestPythonCLIHelperParity(t *testing.T) {
	var fixture struct {
		Timing []struct {
			Kind, Key, Summary string
			Now                int64
			State              state.State
			Times              map[string]float64
		}
		Models     json.RawMessage
		Choices    []menuOption
		Validation []struct {
			Provider, Model string
			Valid           bool
		}
	}
	data, err := os.ReadFile("testdata/python_cli.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	tracker, err := graph.NewAnalystWallTimeTracker([]string{"market", "news"})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range fixture.Timing {
		now := time.Unix(event.Now, 0)
		if event.Kind == "start" {
			if err = tracker.MarkStarted(event.Key, now); err != nil {
				t.Fatal(err)
			}
		} else {
			tracker.Sync(event.State, now)
		}
		got := map[string]float64{}
		for key, duration := range tracker.WallTimes() {
			got[key] = duration.Seconds()
		}
		if tracker.Summary() != event.Summary || !reflect.DeepEqual(got, event.Times) {
			t.Fatal(event.Now, tracker.Summary(), got)
		}
	}
	for _, item := range fixture.Validation {
		if llm.ValidateModel(item.Provider, item.Model) != item.Valid {
			t.Errorf("Python validation mismatch: %s/%s", item.Provider, item.Model)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":` + string(fixture.Models) + `}`))
	}))
	defer server.Close()
	choices, err := fetchOpenRouterModels(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	choices = append(choices, menuOption{"Custom model ID", "custom"})
	if !reflect.DeepEqual(choices, fixture.Choices) {
		t.Fatal(choices, fixture.Choices)
	}
}
