package config

import (
	"testing"
)

func TestEnv(t *testing.T) {
	c, e := Default()
	if e != nil {
		t.Fatal(e)
	}
	env := map[string]string{"TRADINGAGENTS_MAX_DEBATE_ROUNDS": "3", "TRADINGAGENTS_CHECKPOINT_ENABLED": "yes", "TRADINGAGENTS_TEMPERATURE": "0.2", "TRADINGAGENTS_MAX_TOKENS": "128"}
	if e = c.ApplyEnv(func(s string) string { return env[s] }); e != nil {
		t.Fatal(e)
	}
	if c.MaxDebateRounds != 3 || !c.CheckpointEnabled || *c.Temperature != 0.2 || *c.MaxTokens != 128 {
		t.Fatal(c)
	}
	env["TRADINGAGENTS_CHECKPOINT_ENABLED"] = "treu"
	if c.ApplyEnv(func(s string) string { return env[s] }) == nil {
		t.Fatal("accepted invalid boolean")
	}
}
