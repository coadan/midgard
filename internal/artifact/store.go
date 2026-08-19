package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Store struct{ root string }

type Artifact struct {
	Ref  string
	Path string
	Size int64
}

func Open(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "sha256"), 0o700); err != nil {
		return nil, err
	}
	return &Store{root: abs}, nil
}

func (s *Store) Put(reader io.Reader) (Artifact, error) {
	w, err := s.NewWriter()
	if err != nil {
		return Artifact{}, err
	}
	if _, err := io.Copy(w, reader); err != nil {
		w.Abort()
		return Artifact{}, err
	}
	return w.Seal()
}

type Writer struct {
	store  *Store
	file   *os.File
	hash   hash.Hash
	size   int64
	closed bool
}

func (s *Store) NewWriter() (*Writer, error) {
	file, err := os.CreateTemp(s.root, ".unsealed-*")
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	return &Writer{store: s, file: file, hash: sha256.New()}, nil
}

func (w *Writer) Write(data []byte) (int, error) {
	if w.closed {
		return 0, errors.New("artifact writer is closed")
	}
	n, err := w.file.Write(data)
	if n > 0 {
		_, _ = w.hash.Write(data[:n])
		w.size += int64(n)
	}
	return n, err
}

func (w *Writer) Seal() (Artifact, error) {
	if w.closed {
		return Artifact{}, errors.New("artifact writer is closed")
	}
	w.closed = true
	if err := w.file.Sync(); err != nil {
		w.abortClosed()
		return Artifact{}, err
	}
	if err := w.file.Close(); err != nil {
		os.Remove(w.file.Name())
		return Artifact{}, err
	}
	digest := hex.EncodeToString(w.hash.Sum(nil))
	dir := filepath.Join(w.store.root, "sha256", digest[:2])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		os.Remove(w.file.Name())
		return Artifact{}, err
	}
	target := filepath.Join(dir, digest[2:])
	if err := os.Link(w.file.Name(), target); err != nil {
		if !errors.Is(err, os.ErrExist) {
			os.Remove(w.file.Name())
			return Artifact{}, err
		}
		info, statErr := os.Stat(target)
		if statErr != nil || info.Size() != w.size {
			os.Remove(w.file.Name())
			return Artifact{}, fmt.Errorf("existing artifact does not match content length")
		}
	}
	os.Remove(w.file.Name())
	if err := os.Chmod(target, 0o400); err != nil {
		return Artifact{}, err
	}
	return Artifact{Ref: "sha256:" + digest, Path: target, Size: w.size}, nil
}

func (w *Writer) Abort() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.abortClosed()
}

func (w *Writer) abortClosed() error {
	name := w.file.Name()
	err := w.file.Close()
	removeErr := os.Remove(name)
	if err != nil {
		return err
	}
	return removeErr
}

func (s *Store) Open(ref string) (*os.File, error) {
	path, err := s.Path(ref)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *Store) Path(ref string) (string, error) {
	if len(ref) != len("sha256:")+64 || !strings.HasPrefix(ref, "sha256:") {
		return "", errors.New("invalid artifact reference")
	}
	digest := strings.TrimPrefix(ref, "sha256:")
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return "", errors.New("invalid artifact digest")
	}
	return filepath.Join(s.root, "sha256", digest[:2], digest[2:]), nil
}

func (s *Store) Verify(ref string) error {
	file, err := s.Open(ref)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	want := strings.TrimPrefix(ref, "sha256:")
	if got := hex.EncodeToString(digest.Sum(nil)); got != want {
		return fmt.Errorf("artifact checksum mismatch: got %s", got)
	}
	return nil
}
