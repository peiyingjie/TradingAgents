package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelpNeedsNoConfiguration(t *testing.T) {
	t.Setenv("TRADINGAGENTS_MAX_DEBATE_ROUNDS", "invalid")
	for _, args := range [][]string{{"--help"}, {"backtest", "--help"}} {
		var out bytes.Buffer
		if e := Run(context.Background(), args, strings.NewReader(""), &out, &out); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(out.String(), "Usage: tradingagents") {
			t.Fatal(out.String())
		}
	}
}
