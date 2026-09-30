package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

//go:embed known_models.json
var knownModelsJSON []byte

// ValidateModel preserves the reference's advisory validation. Custom IDs are
// accepted by providers whose catalogs are intentionally open-ended.
func ValidateModel(provider, name string) bool {
	provider = strings.ToLower(provider)
	switch provider {
	case "ollama", "openrouter", "openai_compatible", "mistral", "kimi", "groq", "nvidia", "bedrock", "azure":
		return true
	}
	var catalog map[string][]string
	if err := json.Unmarshal(knownModelsJSON, &catalog); err != nil {
		return true
	}
	models, known := catalog[provider]
	return !known || slices.Contains(models, name)
}

func warnUnknownModel(provider, name string) {
	if !ValidateModel(provider, name) {
		slog.Warn(fmt.Sprintf("Model '%s' is not in the known model list for provider '%s'. Continuing anyway.", name, provider))
	}
}
