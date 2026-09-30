package graph

import (
	"testing"
	"time"
	"tradingagents/internal/state"
)

func TestAnalystTimingStartResumeAndCompletion(t *testing.T) {
	tracker, err := NewAnalystWallTimeTracker([]string{"market", "news"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	if tracker.Summary() != "Analyst wall time: pending" {
		t.Fatal(tracker.Summary())
	}
	if err = tracker.MarkStarted("market", now); err != nil {
		t.Fatal(err)
	}
	tracker.Sync(state.State{}, now.Add(time.Second))
	tracker.Sync(state.State{MarketReport: "0"}, now.Add(10*time.Second))
	tracker.Sync(state.State{MarketReport: "0", NewsReport: "news"}, now.Add(12*time.Second))
	tracker.Sync(state.State{MarketReport: "0", NewsReport: "news"}, now.Add(20*time.Second))
	if tracker.Summary() != "Analyst wall time: Market 10.00s | News 2.00s" {
		t.Fatal(tracker.Summary())
	}
	copy := tracker.WallTimes()
	copy["market"] = 0
	if tracker.WallTimes()["market"] != 10*time.Second {
		t.Fatal("mutable view")
	}
	resumed, _ := NewAnalystWallTimeTracker([]string{"market", "news"})
	resumed.Sync(state.State{MarketReport: "restored"}, now)
	if resumed.WallTimes()["market"] != 0 {
		t.Fatal(resumed.WallTimes())
	}
	if err = resumed.MarkCompleted("news", now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if resumed.WallTimes()["news"] != 0 {
		t.Fatal("negative duration")
	}
	if err = resumed.MarkStarted("missing", now); err == nil {
		t.Fatal("unknown analyst accepted")
	}
	if err = resumed.MarkCompleted("missing", now); err == nil {
		t.Fatal("unknown analyst accepted")
	}
}
