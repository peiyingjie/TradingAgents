package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

const announcementsURL = "https://api.tauric.ai/v1/announcements"
const announcementsFallback = "For more information, please visit https://github.com/TauricResearch"
const openRouterModelsURL = "https://openrouter.ai/api/v1/models"

var richTags = regexp.MustCompile(`\[(?:/?(?:cyan|green|yellow|red|blue|magenta|bold|dim|link)|link=[^\]]+)\]`)

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(into)
}

type announcement struct {
	Announcements    []string `json:"announcements"`
	RequireAttention bool     `json:"require_attention"`
}

func fetchAnnouncements(ctx context.Context, client *http.Client, endpoint string) announcement {
	fallback := announcement{Announcements: []string{announcementsFallback}}
	result := fallback
	if err := fetchJSON(ctx, client, endpoint, &result); err != nil {
		return fallback
	}
	return result
}
func (t terminal) showAnnouncements(ctx context.Context) error {
	data := fetchAnnouncements(ctx, &http.Client{Timeout: time.Second}, announcementsURL)
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data.Announcements) == 0 {
		return nil
	}
	lines := []string{}
	for _, entry := range data.Announcements {
		for _, line := range strings.Split(entry, "\n") {
			lines = append(lines, safeLine(richTags.ReplaceAllString(line, ""), 1<<20))
		}
	}
	fmt.Fprintln(t.Out, "\nAnnouncements\n"+strings.Join(lines, "\n"))
	if data.RequireAttention {
		_, err := t.ask("Press Enter to continue", "")
		return err
	}
	return nil
}

type menuOption struct{ Label, Value string }

func fetchOpenRouterModels(ctx context.Context, client *http.Client, endpoint string) ([]menuOption, error) {
	var response struct {
		Data []struct {
			ID, Name string
			Created  int64
		}
	}
	if err := fetchJSON(ctx, client, endpoint, &response); err != nil {
		return nil, err
	}
	sort.SliceStable(response.Data, func(i, j int) bool { return response.Data[i].Created > response.Data[j].Created })
	all, mainstream := []menuOption{}, []menuOption{}
	namespaces := []string{"openai", "anthropic", "google", "deepseek", "qwen", "mistralai", "meta-llama", "x-ai", "z-ai", "minimax", "moonshotai"}
	for _, m := range response.Data {
		if m.ID == "" {
			continue
		}
		label := m.Name
		if label == "" {
			label = m.ID
		}
		option := menuOption{label, m.ID}
		all = append(all, option)
		prefix, _, _ := strings.Cut(m.ID, "/")
		for _, ns := range namespaces {
			if prefix == ns && !strings.HasPrefix(m.ID, "~") {
				mainstream = append(mainstream, option)
				break
			}
		}
	}
	if len(mainstream) > 0 {
		all = mainstream
	}
	return all[:min(5, len(all))], nil
}
func (t terminal) selectModel(ctx context.Context, provider, mode, remembered string) (string, error) {
	if provider == "azure" {
		return t.requireText("Enter Azure deployment name ("+mode+"-thinking)", "")
	}
	options := []menuOption{}
	if provider == "openrouter" {
		var err error
		options, err = fetchOpenRouterModels(ctx, &http.Client{Timeout: 10 * time.Second}, openRouterModelsURL)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			fmt.Fprintf(t.Out, "Could not fetch OpenRouter models: %v\n", err)
		}
		options = append(options, menuOption{"Custom model ID", "custom"})
		remembered = ""
	} else {
		var catalog map[string]map[string][][2]string
		if err := json.Unmarshal(modelsJSON, &catalog); err != nil {
			return "", err
		}
		for _, o := range catalog[strings.TrimSuffix(provider, "-cn")][mode] {
			options = append(options, menuOption{o[0], o[1]})
		}
		if len(options) == 0 {
			options = append(options, menuOption{"Custom model ID", "custom"})
		}
	}
	label := mode + " thinking model"
	if provider == "openrouter" {
		label += " (OpenRouter latest available)"
	}
	name, err := t.choose(label, options, remembered)
	if err != nil {
		return "", err
	}
	if name == "custom" {
		return t.requireText("Custom model ID", "")
	}
	return name, nil
}
