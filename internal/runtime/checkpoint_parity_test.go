package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadPythonCheckpointAndRoundTripSignedMetadata(t *testing.T) {
	b, e := os.ReadFile("testdata/python_checkpoint.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		State    json.RawMessage
		NextNode string `json:"next_node"`
		Step     int
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	store, e := OpenSQLite(ctx, filepath.Join(t.TempDir(), "reference.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if _, e = store.db.ExecContext(ctx, "INSERT INTO runtime_checkpoints VALUES (?,1,?,?,?)", "python", string(fixture.State), fixture.NextNode, fixture.Step); e != nil {
		t.Fatal(e)
	}
	saved, e := store.Load(ctx, "python")
	if e != nil {
		t.Fatal(e)
	}
	if saved.NextNode != "tools_news" || saved.Step != 3 || len(saved.State.Messages) != 1 || saved.State.Messages[0].ToolCalls[0].ID != "call-1" {
		t.Fatalf("%+v", saved)
	}
	if !strings.Contains(string(saved.State.Messages[0].Additional["google_parts"]), "c2lnbmVk") {
		t.Fatal("signed bytes lost")
	}
	if e = store.Save(ctx, "go", *saved); e != nil {
		t.Fatal(e)
	}
	reloaded, e := store.Load(ctx, "go")
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(saved, reloaded) {
		t.Fatal("checkpoint roundtrip changed state")
	}
}
