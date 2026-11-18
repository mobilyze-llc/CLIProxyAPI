package registry

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestAntigravityModelFallbacksYieldToRemoteCatalog(t *testing.T) {
	original := getModels()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})

	var remote map[string][]map[string]any
	if err := json.Unmarshal(embeddedModelsJSON, &remote); err != nil {
		t.Fatal(err)
	}
	remote["antigravity"] = []map[string]any{
		{"id": "gemini-4-argon-high", "display_name": "Remote Argon"},
		{"id": "gemini-4-neon-high", "display_name": "Remote Neon"},
	}
	data, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishCatalogBytes(data); err != nil {
		t.Fatal(err)
	}

	models := make(map[string]*ModelInfo)
	for _, model := range GetAntigravityModels() {
		if models[model.ID] != nil {
			t.Fatalf("duplicate model %q", model.ID)
		}
		models[model.ID] = model
	}
	for id, name := range map[string]string{
		"gemini-4-argon-high": "Remote Argon",
		"gemini-4-neon-high":  "Remote Neon",
	} {
		if model := models[id]; model == nil || model.DisplayName != name {
			t.Fatalf("%s = %+v, want remote display name %q", id, model, name)
		}
	}
	for _, tier := range []string{"low", "medium"} {
		model := models["gemini-4-neon-"+tier]
		if model == nil || model.Thinking == nil || !slices.Equal(model.Thinking.Levels, []string{tier}) {
			t.Fatalf("missing Neon %s fallback or incorrect reasoning levels: %+v", tier, model)
		}
	}
}
