package reporting

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"tradingagents/internal/state"
)

func TestPythonReportTree(t *testing.T) {
	b, e := os.ReadFile("testdata/python_reports.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		State state.State
		Files map[string]string
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	path, e := Write(fixture.State, "AAPL", dir, time.Date(2026, 8, 14, 12, 34, 56, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	if path != filepath.Join(dir, "complete_report.md") {
		t.Fatal(path)
	}
	got := map[string]string{}
	e = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(dir, path)
		if e != nil {
			return e
		}
		got[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, fixture.Files) {
		t.Fatal("report content or file layout differs from Python")
	}
}
