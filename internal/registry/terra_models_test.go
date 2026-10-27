package registry

import "testing"

func TestCodexCatalogsIncludeTerraWhenRemoteCatalogLags(t *testing.T) {
	for name, getModels := range map[string]func() []*ModelInfo{
		"free": GetCodexFreeModels,
		"team": GetCodexTeamModels,
		"plus": GetCodexPlusModels,
		"pro":  GetCodexProModels,
	} {
		t.Run(name, func(t *testing.T) {
			for _, model := range getModels() {
				if model.ID == "gpt-6.1-terra" {
					if model.ContextLength != 500000 || len(model.Thinking.Levels) != 6 {
						t.Fatalf("unexpected Terra capabilities: %+v", model)
					}
					return
				}
			}
			t.Fatal("Terra is absent from the Codex model catalog")
		})
	}
}

func TestCodexTerraPreservesNewerRemoteMetadata(t *testing.T) {
	remote := &ModelInfo{ID: "gpt-6.1-terra", ContextLength: 900000}
	count := 0
	for _, model := range WithCodexBuiltins([]*ModelInfo{remote}) {
		if model.ID == remote.ID {
			count++
			if model.ContextLength != remote.ContextLength {
				t.Fatal("built-in fallback replaced remote metadata")
			}
		}
	}
	if count != 1 {
		t.Fatalf("Terra entries=%d, want1", count)
	}
}
