package portfolio

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPythonPortfolioParity(t *testing.T) {
	b, e := os.ReadFile("testdata/python_portfolios.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Input               json.RawMessage
		Render, Fingerprint string
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		p := Portfolio{Positions: []Position{}}
		if e = json.Unmarshal(c.Input, &p); e != nil {
			t.Fatal(e)
		}
		if got := p.Render("AAPL"); got != c.Render {
			t.Errorf("render: got %q want %q", got, c.Render)
		}
		got, e := p.Fingerprint()
		if e != nil {
			t.Fatal(e)
		}
		if got != c.Fingerprint {
			t.Errorf("fingerprint %s got %s want %s", c.Input, got, c.Fingerprint)
		}
	}
}

func TestPortfolioRejectsMissingAndNullRequiredFields(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"positions":null}`, `{"positions":[null]}`, `{"positions":[{"ticker":null,"quantity":1}]}`, `{"positions":[{"ticker":"AAPL","quantity":null}]}`, `{"positions":[{"ticker":"AAPL"}]}`} {
		var p Portfolio
		if e := json.Unmarshal([]byte(raw), &p); e == nil {
			t.Errorf("accepted invalid portfolio: %s", raw)
		}
	}
}
