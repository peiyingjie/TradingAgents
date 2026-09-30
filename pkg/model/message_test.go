package model

import (
	"encoding/json"
	"testing"
)

func TestMessageReducer(t *testing.T) {
	a := NewMessage("user", "old")
	a.ID = "a"
	b := NewMessage("assistant", "new")
	b.ID = "a"
	out, e := AddMessages([]Message{a}, []Message{{Type: "remove", ID: "a"}, b, NewMessage("tool", "result")})
	if e != nil || len(out) != 2 || out[0].Content.String() != "new" || out[1].ID == "" {
		t.Fatalf("%+v %v", out, e)
	}
	if _, e = AddMessages(nil, []Message{{Type: "remove", ID: "missing"}}); e == nil {
		t.Fatal("unknown removal accepted")
	}
	if a.Content.String() != "old" {
		t.Fatal("mutated input")
	}
}
func TestContentRoundtrip(t *testing.T) {
	var m Message
	if e := json.Unmarshal([]byte(`{"type":"ai","content":[{"type":"text","text":"hello"}],"additional_kwargs":{"signature":"signed"}}`), &m); e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	var n Message
	if e = json.Unmarshal(b, &n); e != nil {
		t.Fatal(e)
	}
	if n.Content.String() != "hello" || string(n.Additional["signature"]) != `"signed"` {
		t.Fatal(n)
	}
}
