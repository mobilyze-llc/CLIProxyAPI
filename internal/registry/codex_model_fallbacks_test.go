package registry

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestCodexPaidModelFallbacksYieldToRemoteCatalog(t *testing.T) {
	original := getModels()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})

	var remote map[string][]map[string]any
	if err := json.Unmarshal(embeddedModelsJSON, &remote); err != nil {
		t.Fatalf("decode embedded models: %v", err)
	}
	remote["claude"][0]["display_name"] = "Remote Claude"
	for _, tier := range []string{"codex-free", "codex-team", "codex-plus", "codex-pro"} {
		remote[tier] = slices.DeleteFunc(remote[tier], func(model map[string]any) bool {
			return model["id"] == "gpt-5.5" || model["id"] == "gpt-6.2-sol" || model["id"] == "gpt-6.2-astra"
		})
		remote[tier] = append(remote[tier], map[string]any{
			"id": "gpt-5.5", "object": "model", "owned_by": "openai", "type": "openai",
			"description": "Remote gpt-5.5",
		})
	}
	remote["codex-pro"] = append(remote["codex-pro"], map[string]any{
		"id": "gpt-6.2-sol", "object": "model", "owned_by": "openai", "type": "openai",
		"description": "Upstream Sol",
	})
	data, err := json.Marshal(remote)
	if err != nil {
		t.Fatalf("encode remote models: %v", err)
	}
	if _, err = publishCatalogBytes(data); err != nil {
		t.Fatalf("publish remote models: %v", err)
	}

	if got := GetClaudeModels()[0].DisplayName; got != "Remote Claude" {
		t.Fatalf("Claude display name = %q, want remote change", got)
	}
	tiers := map[string][]*ModelInfo{
		"free": GetCodexFreeModels(),
		"team": GetCodexTeamModels(),
		"plus": GetCodexPlusModels(),
		"pro":  GetCodexProModels(),
	}
	for tier, models := range tiers {
		byID := make(map[string]*ModelInfo, len(models))
		for _, model := range models {
			if _, exists := byID[model.ID]; exists {
				t.Fatalf("%s: duplicate model %q", tier, model.ID)
			}
			byID[model.ID] = model
		}
		if got := byID["gpt-5.5"]; got == nil || got.Description != "Remote gpt-5.5" {
			t.Fatalf("%s: gpt-5.5 = %+v, want remote change", tier, got)
		}
		sol, astra := byID["gpt-6.2-sol"], byID["gpt-6.2-astra"]
		if tier == "free" {
			if sol != nil || astra != nil {
				t.Fatalf("free exposes 6.2 models")
			}
			continue
		}
		if sol == nil || astra == nil {
			t.Fatalf("%s: 6.2 models missing", tier)
		}
		wantSol := "Latest workhorse model for coding and everyday work."
		if tier == "pro" {
			wantSol = "Upstream Sol"
		}
		if sol.Description != wantSol {
			t.Fatalf("%s: gpt-6.2-sol description = %q, want %q", tier, sol.Description, wantSol)
		}
		if astra.NativeCapabilities == nil || astra.NativeCapabilities.WebSearch == nil ||
			!*astra.NativeCapabilities.WebSearch ||
			!astra.SupportConfigurationUpdate || !slices.Contains(astra.Thinking.Levels, "ultra") {
			t.Fatalf("%s: gpt-6.2-astra lost native metadata: %+v", tier, astra)
		}
	}

	for _, model := range tiers["team"] {
		if model.ID == "gpt-6.2-astra" {
			model.ID = "mutated"
		}
	}
	for _, model := range GetCodexTeamModels() {
		if model.ID == "mutated" {
			t.Fatal("caller mutation reached shared fallback models")
		}
	}
}

func TestCodexClientModelFallbacksYieldToRemoteCatalog(t *testing.T) {
	original, _ := GetCodexClientModelsSnapshot()
	t.Cleanup(func() {
		if _, err := loadCodexClientModelsFromBytes(original, "test cleanup"); err != nil {
			t.Fatalf("restore original catalog: %v", err)
		}
	})

	fallbackData, err := os.ReadFile("models/codex_client_model_fallbacks.json")
	if err != nil {
		t.Fatalf("read native fallback fixture: %v", err)
	}
	var fallbacks []map[string]any
	if err := json.Unmarshal(fallbackData, &fallbacks); err != nil {
		t.Fatalf("decode fallbacks: %v", err)
	}
	var embedded struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &embedded); err != nil {
		t.Fatalf("decode embedded catalog: %v", err)
	}
	remoteModels := slices.DeleteFunc(embedded.Models, func(model map[string]any) bool {
		return model["slug"] == "gpt-6.2-sol" || model["slug"] == "gpt-6.2-astra"
	})
	for _, model := range remoteModels {
		if model["slug"] == "gpt-5.5" {
			model["description"] = "Remote gpt-5.5"
		}
	}
	load := func(models []map[string]any) ([]byte, uint64) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"etag": "remote", "models": models})
		if err != nil {
			t.Fatalf("encode remote catalog: %v", err)
		}
		if _, err = loadCodexClientModelsFromBytes(data, "test"); err != nil {
			t.Fatalf("load remote catalog: %v", err)
		}
		snapshot, revision := GetCodexClientModelsSnapshot()
		if _, err = loadCodexClientModelsFromBytes(data, "test reload"); err != nil {
			t.Fatalf("reload remote catalog: %v", err)
		}
		reloaded, reloadedRevision := GetCodexClientModelsSnapshot()
		if string(reloaded) != string(snapshot) || reloadedRevision != revision {
			t.Fatalf("identical reload changed snapshot: revision %d -> %d", revision, reloadedRevision)
		}
		return snapshot, revision
	}
	decode := func(snapshot []byte) (map[string]any, []map[string]any) {
		t.Helper()
		var catalog map[string]any
		var payload struct {
			Models []map[string]any `json:"models"`
		}
		if err := json.Unmarshal(snapshot, &catalog); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		if err := json.Unmarshal(snapshot, &payload); err != nil {
			t.Fatalf("decode snapshot models: %v", err)
		}
		return catalog, payload.Models
	}

	snapshot, _ := load(remoteModels)
	catalog, models := decode(snapshot)
	if catalog["etag"] != "remote" {
		t.Fatalf("top-level field lost: %v", catalog["etag"])
	}
	want := append(append([]map[string]any(nil), remoteModels...), fallbacks...)
	wantData, _ := json.Marshal(want)
	gotData, _ := json.Marshal(models)
	if string(gotData) != string(wantData) {
		t.Fatal("snapshot models are not the remote models followed by both fallbacks")
	}
	if err := ValidateCodexClientModelsJSON(snapshot); err != nil {
		t.Fatalf("merged snapshot invalid: %v", err)
	}

	upstreamSol := testCodexClientModel("gpt-6.2-sol", 1)
	upstreamSol["description"] = "Upstream Sol"
	snapshot, _ = load(append(remoteModels, upstreamSol))
	_, models = decode(snapshot)
	var slugs []any
	for _, model := range models {
		slugs = append(slugs, model["slug"])
	}
	if got := slugs[len(slugs)-2:]; got[0] != "gpt-6.2-sol" || got[1] != "gpt-6.2-astra" {
		t.Fatalf("trailing slugs = %v", got)
	}
	if models[len(models)-2]["description"] != "Upstream Sol" {
		t.Fatal("upstream gpt-6.2-sol did not supersede fallback")
	}
	if models[len(models)-1]["base_instructions"] != fallbacks[1]["base_instructions"] {
		t.Fatal("fallback gpt-6.2-astra lost native instructions")
	}
}
