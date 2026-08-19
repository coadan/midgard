package credential

import (
	"errors"
	"testing"

	keyring "github.com/zalando/go-keyring"
)

type memoryBackend struct {
	values map[string]string
}

func (m *memoryBackend) Set(service, account, secret string) error {
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[service+":"+account] = secret
	return nil
}

func (m *memoryBackend) Get(service, account string) (string, error) {
	secret, ok := m.values[service+":"+account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return secret, nil
}

func (m *memoryBackend) Delete(service, account string) error {
	key := service + ":" + account
	if _, ok := m.values[key]; !ok {
		return keyring.ErrNotFound
	}
	delete(m.values, key)
	return nil
}

func TestProfilesKeepProviderKeysIndependent(t *testing.T) {
	backend := &memoryBackend{}
	store := NewStoreWithBackend(backend)
	work := Ref{Provider: "DeepSeek", Profile: "work", Name: APIKey}
	personal := Ref{Provider: "deepseek", Profile: "personal", Name: APIKey}
	if err := store.Set(work, "work-secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(personal, "personal-secret"); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[Ref]string{work: "work-secret", personal: "personal-secret"} {
		got, err := store.Get(ref)
		if err != nil || got != want {
			t.Fatalf("Get(%+v) = %q, %v", ref, got, err)
		}
	}
}

func TestEmptyProfileUsesDefault(t *testing.T) {
	account, err := (Ref{Provider: "deepseek", Name: APIKey}).Account()
	if err != nil || account != "deepseek/default/api-key" {
		t.Fatalf("Account() = %q, %v", account, err)
	}
}

func TestMissingCredentialHasStableError(t *testing.T) {
	store := NewStoreWithBackend(&memoryBackend{})
	_, err := store.Get(Ref{Provider: "deepseek", Name: APIKey})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestInvalidReferenceDoesNotReachBackend(t *testing.T) {
	store := NewStoreWithBackend(&memoryBackend{})
	if err := store.Set(Ref{Provider: "../deepseek", Name: APIKey}, "secret"); err == nil {
		t.Fatal("expected invalid provider error")
	}
}
