package retrieval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddingSettingsProduceCredentialFreeYggConfiguration(t *testing.T) {
	catalog := Catalog{Path: filepath.Join(t.TempDir(), "retrieval.json")}
	embedding := Embedding{Endpoint: "https://embeddings.example/v1/embeddings", Model: "text-embedding", Dimensions: 1536, Provider: "openai", Profile: "work", Credential: "api-key"}
	if err := catalog.SetEmbedding(embedding); err != nil {
		t.Fatal(err)
	}
	settings, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	configuration, enabled, err := settings.YggConfig()
	if err != nil || !enabled {
		t.Fatalf("YggConfig() = %s, %v, %v", configuration, enabled, err)
	}
	for _, unexpected := range []string{`"provider"`, `"profile"`, `"credential"`, "work", "api-key"} {
		if strings.Contains(string(configuration), unexpected) {
			t.Fatalf("Ygg config leaked Midgard credential metadata %q: %s", unexpected, configuration)
		}
	}
	var value struct {
		Schema    string `json:"schema"`
		Embedding struct {
			Kind      string `json:"kind"`
			APIKeyEnv string `json:"apiKeyEnv"`
			TimeoutMS int    `json:"timeoutMs"`
		} `json:"embedding"`
	}
	if err := json.Unmarshal(configuration, &value); err != nil {
		t.Fatal(err)
	}
	if value.Schema != yggConfigurationSchema || value.Embedding.Kind != "openai-compatible" || value.Embedding.APIKeyEnv != EmbeddingKeyEnvironment || value.Embedding.TimeoutMS != defaultEmbeddingTimeoutMS {
		t.Fatalf("Ygg config = %#v", value)
	}
	stored, err := os.ReadFile(catalog.Path)
	if err != nil || strings.Contains(string(stored), "MIDGARD_YGG_EMBEDDING_API_KEY") {
		t.Fatalf("stored settings leaked runtime key environment: %s, %v", stored, err)
	}
}

func TestEmbeddingSettingsRejectInvalidCredentialReference(t *testing.T) {
	catalog := Catalog{Path: filepath.Join(t.TempDir(), "retrieval.json")}
	err := catalog.SetEmbedding(Embedding{Endpoint: "https://embeddings.example/v1/embeddings", Model: "text-embedding", Dimensions: 1536, Provider: "Open AI", Credential: "api-key"})
	if err == nil || !strings.Contains(err.Error(), "credential reference") {
		t.Fatalf("SetEmbedding error = %v", err)
	}
}
