package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

func (s Store) PutFile(record Record, source string) (Record, error) {
	path, err := s.Resolve(record.Path)
	if err != nil {
		return record, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return record, err
	}
	file, err := os.Open(source)
	if err != nil {
		return record, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return record, copyErr
	}
	if closeErr != nil {
		return record, closeErr
	}
	if err := os.Rename(source, path); err != nil {
		return record, err
	}
	record.Size = size
	if record.State == StateSealed {
		record.Checksum = "sha256:" + hex.EncodeToString(hash.Sum(nil))
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

func (s Store) ReadHeadTail(path string, limit int64) (head, tail []byte, size int64, err error) {
	resolved, err := s.Resolve(path)
	if err != nil {
		return nil, nil, 0, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, nil, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, 0, err
	}
	size = info.Size()
	if limit <= 0 {
		return nil, nil, size, nil
	}
	if size <= limit {
		data, err := io.ReadAll(file)
		return data, nil, size, err
	}
	headSize := limit * 2 / 3
	tailSize := limit - headSize
	head = make([]byte, headSize)
	if _, err := io.ReadFull(file, head); err != nil {
		return nil, nil, size, err
	}
	if _, err := file.Seek(-tailSize, io.SeekEnd); err != nil {
		return nil, nil, size, err
	}
	tail = make([]byte, tailSize)
	if _, err := io.ReadFull(file, tail); err != nil {
		return nil, nil, size, err
	}
	return head, tail, size, nil
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
