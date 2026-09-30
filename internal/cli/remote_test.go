package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"tradingagents/internal/runtime"
	"tradingagents/internal/state"
	"tradingagents/pkg/model"
)

func testTerminal(input string, out *bytes.Buffer) terminal {
	return terminal{Reader: bufio.NewReader(strings.NewReader(input)), Input: strings.NewReader(input), Out: out, Context: context.Background()}
}

func TestOpenRouterDiscoverySelectionAndFallback(t *testing.T) {
	for _, test := range []struct {
		name, body, input, want string
		status                  int
	}{
		{"mainstream newest", "{\"data\":[{\"id\":\"niche/new\",\"created\":999},{\"id\":\"openai/old\",\"created\":1},{\"id\":\"~openai/alias\",\"created\":1000},{\"id\":\"google/latest\",\"name\":\"Latest\",\"created\":2}]}", "\n", "google/latest", 200},
		{"no mainstream", `{"data":[{"id":"niche/old","created":null},{"id":"niche/new","created":5}]}`, "\n", "niche/new", 200},
		{"empty custom", `{"data":[]}`, "\nmy/model\n", "my/model", 200},
		{"http fallback", "error", "\nmy/model\n", "my/model", 503},
		{"json fallback", "bad", "\nmy/model\n", "my/model", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/models" {
					t.Error(r.URL.Path)
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("catalog must not send credentials")
				}
				w.WriteHeader(test.status)
				w.Write([]byte(test.body))
			}))
			defer server.Close()
			original := http.DefaultTransport
			http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != openRouterModelsURL {
					return nil, errors.New("external network blocked")
				}
				copy := r.Clone(r.Context())
				u := *r.URL
				copy.URL = &u
				copy.URL.Scheme = "http"
				copy.URL.Host = strings.TrimPrefix(server.URL, "http://")
				return original.RoundTrip(copy)
			})
			t.Cleanup(func() { http.DefaultTransport = original })
			var out bytes.Buffer
			value, err := testTerminal(test.input, &out).selectModel(context.Background(), "openrouter", "quick", "remembered/ignored")
			if err != nil || value != test.want {
				t.Fatal(value, err, out.String())
			}
		})
	}
	t.Run("limit and cancellation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":[{"id":"openai/1"},{"id":"openai/2"},{"id":"openai/3"},{"id":"openai/4"},{"id":"openai/5"},{"id":"openai/6"}]}`))
		}))
		defer server.Close()
		options, err := fetchOpenRouterModels(context.Background(), server.Client(), server.URL)
		if err != nil || len(options) != 5 {
			t.Fatal(options, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err = fetchOpenRouterModels(ctx, server.Client(), server.URL); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestAnnouncementsFallbackAttentionAndTimeout(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		attention  bool
		count      int
	}{
		{"attention", `{"announcements":["Read this"],"require_attention":true}`, 200, true, 1},
		{"silent", `{"announcements":[]}`, 200, false, 0},
		{"missing", `{}`, 200, false, 1},
		{"malformed", `{`, 200, false, 1},
		{"http", `{}`, 503, false, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); w.Write([]byte(test.body)) }))
			defer server.Close()
			got := fetchAnnouncements(context.Background(), server.Client(), server.URL)
			if got.RequireAttention != test.attention || len(got.Announcements) != test.count {
				t.Fatal(got)
			}
			original := http.DefaultTransport
			http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != announcementsURL {
					return nil, errors.New("external network blocked")
				}
				copy := r.Clone(r.Context())
				u := *r.URL
				copy.URL = &u
				copy.URL.Scheme = "http"
				copy.URL.Host = strings.TrimPrefix(server.URL, "http://")
				return original.RoundTrip(copy)
			})
			defer func() { http.DefaultTransport = original }()
			var out bytes.Buffer
			ui := testTerminal("\nNEXT\n", &out)
			if err := ui.showAnnouncements(context.Background()); err != nil {
				t.Fatal(err)
			}
			next, _ := ui.Reader.ReadString('\n')
			if test.attention && next != "NEXT\n" {
				t.Fatal("attention did not consume Enter", next)
			}
			if !test.attention && next != "\n" {
				t.Fatal("unnecessary attention prompt", next)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := fetchAnnouncements(ctx, &http.Client{Timeout: time.Millisecond}, "http://127.0.0.1:1")
	if len(got.Announcements) != 1 || got.Announcements[0] != announcementsFallback || got.RequireAttention {
		t.Fatal(got)
	}
}

func TestSelectionCancellationOrderAndReports(t *testing.T) {
	var out bytes.Buffer
	selected, err := testTerminal("news,market,market,fundamentals\n", &out).selectAnalysts("crypto", nil)
	if err != nil || strings.Join(selected, ",") != "market,news" {
		t.Fatal(selected, err)
	}
	if _, err = testTerminal("\x1b\n", &out).selectModel(context.Background(), "azure", "quick", ""); !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	if _, err = testTerminal("\n", &out).selectModel(context.Background(), "azure", "quick", ""); err == nil {
		t.Fatal("empty deployment accepted")
	}
	language, err := testTerminal("\x03\n", &out).selectLanguage("Chinese")
	if language != "English" || err != nil {
		t.Fatal(language, err)
	}
	provider, err := testTerminal("qwen\n2\n", &out).selectProvider("")
	if err != nil || provider != "qwen-cn" {
		t.Fatal(provider, err)
	}
	dir := t.TempDir()
	s := state.State{MarketReport: "Evidence", FinalTradeDecision: "**Rating**: Hold"}
	out.Reset()
	if err = testTerminal("N\nY\n", &out).offerReport(s, "AAPL", dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 || !strings.Contains(out.String(), "Evidence") {
		t.Fatal(out.String(), entries)
	}
	out.Reset()
	now := time.Date(2026, 9, 30, 1, 2, 3, 0, time.UTC)
	if err = testTerminal("Y\n\nN\n", &out).offerReport(s, "AAPL", dir, now); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "reports", "AAPL_20260930_010203", "complete_report.md")); err != nil {
		t.Fatal(err)
	}
}

func TestProgressViewDeduplicationAndState(t *testing.T) {
	view := newProgressView([]string{"news", "market"})
	msg := model.NewMessage("ai", "0")
	msg.ID = "same"
	s := state.State{NewsReport: "news", MarketReport: "market", Messages: []model.Message{msg}, FinalTradeDecision: "**Rating**: Hold"}
	update, err := state.AsUpdate(s)
	if err != nil {
		t.Fatal(err)
	}
	event := runtime.Event{Next: runtime.End, State: s, Update: update}
	view.update(event)
	view.update(event)
	if len(view.messages) != 1 || view.messages[0] != "Agent: 0" {
		t.Fatal(view.messages)
	}
	frame := view.frame(100, 35, "LLM calls: 7")
	for _, want := range []string{"Messages & Tools", "Current Report", "LLM calls: 7", "Agents: 10/10"} {
		if !strings.Contains(frame, want) {
			t.Fatal(frame)
		}
	}
	if strings.Contains(safeLine("external\x1b[2J", 80), "\x1b") {
		t.Fatal("untrusted terminal escape retained")
	}
}
