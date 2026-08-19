// Package retrieval owns Midgard's optional semantic repository-search setup.
// It stores only the embedding endpoint and an OS-keyring reference; the API
// key itself is never written to this file or passed through action payloads.
package retrieval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"midgard/internal/credential"
)

const (
	schema                    = "midgard.retrieval/v1"
	EmbeddingKeyEnvironment   = "MIDGARD_YGG_EMBEDDING_API_KEY"
	yggConfigurationSchema    = "ygg.config/v1"
	defaultEmbeddingTimeoutMS = 15_000
)

type Embedding struct {
	Endpoint       string `json:"endpoint"`
	Model          string `json:"model"`
	Dimensions     int    `json:"dimensions"`
	Provider       string `json:"provider"`
	Profile        string `json:"profile"`
	Credential     string `json:"credential"`
	TimeoutMS      int    `json:"timeout_ms,omitempty"`
	BatchSize      int    `json:"batch_size,omitempty"`
	MaxInputChars  int    `json:"max_input_chars,omitempty"`
	QueryPrefix    string `json:"query_prefix,omitempty"`
	DocumentPrefix string `json:"document_prefix,omitempty"`
}

func (e Embedding) CredentialRef() credential.Ref {
	return credential.Ref{Provider: e.Provider, Profile: e.Profile, Name: e.Credential}
}

func (e Embedding) Validate() error {
	endpoint, err := url.ParseRequestURI(strings.TrimSpace(e.Endpoint))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return errors.New("embedding endpoint must be an http or https URL")
	}
	if strings.TrimSpace(e.Model) == "" {
		return errors.New("embedding model is required")
	}
	if e.Dimensions < 1 || e.Dimensions > 65_536 {
		return errors.New("embedding dimensions must be between 1 and 65536")
	}
	if _, err := e.CredentialRef().Account(); err != nil {
		return fmt.Errorf("embedding credential reference: %w", err)
	}
	if e.TimeoutMS < 0 || e.TimeoutMS > 120_000 {
		return errors.New("embedding timeout must be between 1 and 120000 milliseconds when provided")
	}
	if e.BatchSize < 0 || e.BatchSize > 256 {
		return errors.New("embedding batch size must be between 1 and 256 when provided")
	}
	if e.MaxInputChars < 0 || e.MaxInputChars > 100_000 {
		return errors.New("embedding max input characters must be between 1 and 100000 when provided")
	}
	return nil
}

type Settings struct {
	Schema    string     `json:"schema"`
	Embedding *Embedding `json:"embedding,omitempty"`
}

func (s Settings) Validate() error {
	if s.Schema != schema {
		return fmt.Errorf("unsupported retrieval configuration schema %q", s.Schema)
	}
	if s.Embedding != nil {
		return s.Embedding.Validate()
	}
	return nil
}

// YggConfig returns the credential-free configuration passed only to the
// bundled Yggdrasil process. Semantic search is disabled when no embedding is
// configured.
func (s Settings) YggConfig() ([]byte, bool, error) {
	if err := s.Validate(); err != nil {
		return nil, false, err
	}
	configuration := struct {
		Schema    string `json:"schema"`
		Embedding *struct {
			Kind           string `json:"kind"`
			Endpoint       string `json:"endpoint"`
			Model          string `json:"model"`
			Dimensions     int    `json:"dimensions"`
			APIKeyEnv      string `json:"apiKeyEnv"`
			TimeoutMS      int    `json:"timeoutMs,omitempty"`
			BatchSize      int    `json:"batchSize,omitempty"`
			MaxInputChars  int    `json:"maxInputChars,omitempty"`
			QueryPrefix    string `json:"queryPrefix,omitempty"`
			DocumentPrefix string `json:"documentPrefix,omitempty"`
		} `json:"embedding,omitempty"`
	}{Schema: yggConfigurationSchema}
	if s.Embedding == nil {
		data, err := json.Marshal(configuration)
		return data, false, err
	}
	embedding := s.Embedding
	timeout := embedding.TimeoutMS
	if timeout == 0 {
		timeout = defaultEmbeddingTimeoutMS
	}
	configuration.Embedding = &struct {
		Kind           string `json:"kind"`
		Endpoint       string `json:"endpoint"`
		Model          string `json:"model"`
		Dimensions     int    `json:"dimensions"`
		APIKeyEnv      string `json:"apiKeyEnv"`
		TimeoutMS      int    `json:"timeoutMs,omitempty"`
		BatchSize      int    `json:"batchSize,omitempty"`
		MaxInputChars  int    `json:"maxInputChars,omitempty"`
		QueryPrefix    string `json:"queryPrefix,omitempty"`
		DocumentPrefix string `json:"documentPrefix,omitempty"`
	}{
		Kind: "openai-compatible", Endpoint: embedding.Endpoint, Model: embedding.Model, Dimensions: embedding.Dimensions,
		APIKeyEnv: EmbeddingKeyEnvironment, TimeoutMS: timeout, BatchSize: embedding.BatchSize,
		MaxInputChars: embedding.MaxInputChars, QueryPrefix: embedding.QueryPrefix, DocumentPrefix: embedding.DocumentPrefix,
	}
	data, err := json.Marshal(configuration)
	return data, true, err
}

type Catalog struct{ Path string }

func OpenCatalog() (Catalog, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Catalog{}, fmt.Errorf("locate Midgard configuration: %w", err)
	}
	return Catalog{Path: filepath.Join(base, "midgard", "retrieval.json")}, nil
}

func (c Catalog) Load() (Settings, error) {
	settings := Settings{Schema: schema}
	data, err := os.ReadFile(c.Path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read semantic search configuration: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, fmt.Errorf("read semantic search configuration: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Settings{}, fmt.Errorf("read semantic search configuration: %w", err)
	}
	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func (c Catalog) SetEmbedding(embedding Embedding) error {
	settings := Settings{Schema: schema, Embedding: &embedding}
	if err := settings.Validate(); err != nil {
		return err
	}
	return c.write(settings)
}

func (c Catalog) DisableEmbedding() error {
	return c.write(Settings{Schema: schema})
}

func (c Catalog) write(settings Settings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(c.Path), ".retrieval-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, c.Path)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values")
}
