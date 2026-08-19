package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Mount struct {
	Provider   string `json:"provider"`
	Profile    string `json:"profile"`
	Credential string `json:"credential"`
}

type Index struct{ Path string }

func NewIndex() (Index, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return Index{}, err
	}
	return Index{Path: filepath.Join(base, "midgard", "credentials.json")}, nil
}

func (i Index) List() ([]Mount, error) {
	raw, err := os.ReadFile(i.Path)
	if errors.Is(err, os.ErrNotExist) {
		return []Mount{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credential index: %w", err)
	}
	var document struct {
		Mounts []Mount `json:"mounts"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode credential index: %w", err)
	}
	sortMounts(document.Mounts)
	return document.Mounts, nil
}

func (i Index) Add(mount Mount) error {
	account, err := (Ref{Provider: mount.Provider, Profile: mount.Profile, Name: mount.Credential}).Account()
	if err != nil {
		return err
	}
	_ = account
	mounts, err := i.List()
	if err != nil {
		return err
	}
	for _, existing := range mounts {
		if existing == mount {
			return nil
		}
	}
	mounts = append(mounts, mount)
	return i.write(mounts)
}

func (i Index) Remove(mount Mount) error {
	mounts, err := i.List()
	if err != nil {
		return err
	}
	filtered := mounts[:0]
	for _, existing := range mounts {
		if existing != mount {
			filtered = append(filtered, existing)
		}
	}
	return i.write(filtered)
}

func (i Index) write(mounts []Mount) error {
	sortMounts(mounts)
	raw, err := json.MarshalIndent(struct {
		Mounts []Mount `json:"mounts"`
	}{Mounts: mounts}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(i.Path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(i.Path), ".credentials-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, i.Path)
}

func sortMounts(mounts []Mount) {
	sort.Slice(mounts, func(a, b int) bool {
		if mounts[a].Provider != mounts[b].Provider {
			return mounts[a].Provider < mounts[b].Provider
		}
		if mounts[a].Profile != mounts[b].Profile {
			return mounts[a].Profile < mounts[b].Profile
		}
		return mounts[a].Credential < mounts[b].Credential
	})
}
