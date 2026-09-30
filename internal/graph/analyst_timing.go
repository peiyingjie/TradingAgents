package graph

import (
	"fmt"
	"strings"
	"time"
	"tradingagents/internal/state"
)

// AnalystWallTimeTracker follows the Python tracker, including zero-duration
// reports restored from checkpoints and first-start/first-completion semantics.
// The serial CLI event consumer owns it; it does not mutate graph state.
type AnalystWallTimeTracker struct {
	plan    []AnalystSpec
	started map[string]time.Time
	elapsed map[string]time.Duration
}

func NewAnalystWallTimeTracker(selected []string) (*AnalystWallTimeTracker, error) {
	plan, err := Plan(selected)
	if err != nil {
		return nil, err
	}
	return &AnalystWallTimeTracker{plan, map[string]time.Time{}, map[string]time.Duration{}}, nil
}
func (t *AnalystWallTimeTracker) MarkStarted(key string, now time.Time) error {
	if _, ok := AnalystSpecs[key]; !ok {
		return fmt.Errorf("unknown analyst key: %s", key)
	}
	if _, ok := t.started[key]; !ok {
		t.started[key] = now
	}
	return nil
}
func (t *AnalystWallTimeTracker) MarkCompleted(key string, now time.Time) error {
	if _, ok := AnalystSpecs[key]; !ok {
		return fmt.Errorf("unknown analyst key: %s", key)
	}
	if _, ok := t.elapsed[key]; ok {
		return nil
	}
	if start, ok := t.started[key]; ok {
		t.elapsed[key] = max(time.Duration(0), now.Sub(start))
	}
	return nil
}
func (t *AnalystWallTimeTracker) Sync(s state.State, now time.Time) {
	reports := map[string]string{"market": s.MarketReport, "social": s.SentimentReport, "news": s.NewsReport, "fundamentals": s.FundamentalsReport}
	active := false
	for _, spec := range t.plan {
		if reports[spec.Key] != "" {
			_ = t.MarkStarted(spec.Key, now)
			_ = t.MarkCompleted(spec.Key, now)
		} else if !active {
			_ = t.MarkStarted(spec.Key, now)
			active = true
		}
	}
}
func (t *AnalystWallTimeTracker) WallTimes() map[string]time.Duration {
	out := map[string]time.Duration{}
	for k, v := range t.elapsed {
		out[k] = v
	}
	return out
}
func (t *AnalystWallTimeTracker) Summary() string {
	parts := []string{}
	for _, spec := range t.plan {
		if d, ok := t.elapsed[spec.Key]; ok {
			parts = append(parts, fmt.Sprintf("%s %.2fs", strings.TrimSuffix(spec.AgentNode, " Analyst"), d.Seconds()))
		}
	}
	if len(parts) == 0 {
		return "Analyst wall time: pending"
	}
	return "Analyst wall time: " + strings.Join(parts, " | ")
}
