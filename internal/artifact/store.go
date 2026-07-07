package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Store struct {
	Root string
}

func NewStore(root string) Store {
	return Store{Root: root}
}

func (s Store) Put(record Record, data []byte) (Record, error) {
	path, err := s.Resolve(record.Path)
	if err != nil {
		return record, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return record, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return record, err
	}
	record.Size = int64(len(data))
	if record.State == StateSealed {
		sum := sha256.Sum256(data)
		record.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	}
	return record, nil
}

func (s Store) Read(path string) ([]byte, error) {
	resolved, err := s.Resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(resolved)
}

func (s Store) Resolve(path string) (string, error) {
	if err := ValidatePath(path); err != nil {
		return "", err
	}
	return filepath.Join(s.Root, filepath.FromSlash(path)), nil
}

func ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("artifact path is empty")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("artifact path %q is absolute", path)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean != path {
		return fmt.Errorf("artifact path %q is not clean", path)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return fmt.Errorf("artifact path %q escapes artifact root", path)
		}
	}
	return nil
}
