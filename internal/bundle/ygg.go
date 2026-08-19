// Package bundle describes the companion executables that ship with Midgard.
package bundle

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// YggdrasilModule is built as a separate executable because its public
	// contract is its versioned CLI JSON envelope, not Go package imports.
	YggdrasilModule  = "github.com/coadan/yggdrasil"
	YggdrasilVersion = "v0.4.0"
	HeimdalModule    = "github.com/coadan/heimdal"
	HeimdalVersion   = "v0.0.0-20260803075142-786747acf5c4"

	YggdrasilCLIEnvelopeSchema = "ygg.cli/v1"
	YggdrasilSearchSchema      = "ygg.search.result/v4"
	companionManifestSchema    = "midgard.companion/v1"
)

// YggdrasilPath returns the location of the executable bundled beside a
// Midgard binary. It never consults PATH: a session must use the companion
// that was shipped with the running Midgard binary.
func YggdrasilPath(midgardExecutable string) string {
	return companionPath(midgardExecutable, "ygg")
}

// HeimdalPath returns the location of the browser automation executable
// bundled beside a Midgard binary. It never falls back to PATH.
func HeimdalPath(midgardExecutable string) string {
	return companionPath(midgardExecutable, "heimdal")
}

func companionPath(midgardExecutable, name string) string {
	if midgardExecutable == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(midgardExecutable), "libexec", name)
}

type companionManifest struct {
	Schema       string `json:"schema"`
	Name         string `json:"name"`
	Module       string `json:"module"`
	Version      string `json:"version"`
	Sum          string `json:"sum"`
	BinarySHA256 string `json:"binary_sha256"`
}

// ResolveYggdrasil accepts only a companion whose manifest describes the
// pinned module Midgard was built to use.
func ResolveYggdrasil(midgardExecutable string) (string, error) {
	return resolveCompanion(midgardExecutable, "ygg", YggdrasilModule, YggdrasilVersion)
}

// ResolveHeimdal accepts only a companion whose manifest describes the pinned
// Heimdal module Midgard was built to use.
func ResolveHeimdal(midgardExecutable string) (string, error) {
	return resolveCompanion(midgardExecutable, "heimdal", HeimdalModule, HeimdalVersion)
}

func resolveCompanion(midgardExecutable, name, module, version string) (string, error) {
	binary := companionPath(midgardExecutable, name)
	if binary == "" {
		return "", errors.New("Midgard executable path is unavailable")
	}
	info, err := os.Stat(binary)
	if err != nil {
		return "", fmt.Errorf("read bundled %s: %w", name, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("bundled %s is not executable", name)
	}
	raw, err := os.ReadFile(binary + ".manifest.json")
	if err != nil {
		return "", fmt.Errorf("read bundled %s manifest: %w", name, err)
	}
	var manifest companionManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", fmt.Errorf("decode bundled %s manifest: %w", name, err)
	}
	if manifest.Schema != companionManifestSchema || manifest.Name != name || manifest.Module != module || manifest.Version != version || manifest.Sum == "" || manifest.BinarySHA256 == "" {
		return "", fmt.Errorf("bundled %s manifest does not match Midgard's pinned companion", name)
	}
	digest, err := digestFile(binary)
	if err != nil {
		return "", fmt.Errorf("hash bundled %s: %w", name, err)
	}
	if manifest.BinarySHA256 != digest {
		return "", fmt.Errorf("bundled %s digest does not match its manifest", name)
	}
	return binary, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil)), nil
}
