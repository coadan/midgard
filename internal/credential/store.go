// Package credential stores provider secrets outside Midgard's event log,
// artifacts, configuration, and repository worktrees.
package credential

import (
	"errors"
	"fmt"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const (
	Service        = "dev.midgard.provider"
	DefaultProfile = "default"
	APIKey         = "api-key"
)

var ErrNotFound = errors.New("credential not found")

// Ref identifies one secret. Profile allows the same provider to be mounted
// with independent credentials (for example, personal and work accounts).
type Ref struct {
	Provider string
	Profile  string
	Name     string
}

func (r Ref) Account() (string, error) {
	provider, err := validatePart("provider", r.Provider)
	if err != nil {
		return "", err
	}
	profile := r.Profile
	if strings.TrimSpace(profile) == "" {
		profile = DefaultProfile
	}
	profile, err = validatePart("profile", profile)
	if err != nil {
		return "", err
	}
	name, err := validatePart("credential", r.Name)
	if err != nil {
		return "", err
	}
	return provider + "/" + profile + "/" + name, nil
}

func validatePart(kind, value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", fmt.Errorf("%s name is required", kind)
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (index > 0 && (char == '-' || char == '_' || char == '.')) {
			continue
		}
		return "", fmt.Errorf("invalid %s name %q", kind, value)
	}
	return value, nil
}

type Backend interface {
	Set(service, account, secret string) error
	Get(service, account string) (string, error)
	Delete(service, account string) error
}

type nativeBackend struct{}

func (nativeBackend) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}

func (nativeBackend) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}

func (nativeBackend) Delete(service, account string) error {
	return keyring.Delete(service, account)
}

type Store struct{ backend Backend }

func NewStore() Store { return Store{backend: nativeBackend{}} }

func NewStoreWithBackend(backend Backend) Store { return Store{backend: backend} }

func (s Store) Set(ref Ref, secret string) error {
	account, err := ref.Account()
	if err != nil {
		return err
	}
	if s.backend == nil {
		return errors.New("credential backend is required")
	}
	if secret == "" {
		return errors.New("credential value is empty")
	}
	if err := s.backend.Set(Service, account, secret); err != nil {
		return fmt.Errorf("store credential in OS keyring: %w", err)
	}
	return nil
}

func (s Store) Get(ref Ref) (string, error) {
	account, err := ref.Account()
	if err != nil {
		return "", err
	}
	if s.backend == nil {
		return "", errors.New("credential backend is required")
	}
	secret, err := s.backend.Get(Service, account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("read credential from OS keyring: %w", err)
	}
	if secret == "" {
		return "", ErrNotFound
	}
	return secret, nil
}

func (s Store) Exists(ref Ref) (bool, error) {
	_, err := s.Get(ref)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return false, err
}

func (s Store) Delete(ref Ref) error {
	account, err := ref.Account()
	if err != nil {
		return err
	}
	if s.backend == nil {
		return errors.New("credential backend is required")
	}
	if err := s.backend.Delete(Service, account); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete credential from OS keyring: %w", err)
	}
	return nil
}
