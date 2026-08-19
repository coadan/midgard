package credential

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCredentialIndexTracksNonSecretMountMetadata(t *testing.T) {
	index := Index{Path: filepath.Join(t.TempDir(), "credentials.json")}
	work := Mount{Provider: "deepseek", Profile: "work", Credential: APIKey}
	personal := Mount{Provider: "deepseek", Profile: "personal", Credential: APIKey}
	if err := index.Add(work); err != nil {
		t.Fatal(err)
	}
	if err := index.Add(personal); err != nil {
		t.Fatal(err)
	}
	if err := index.Add(work); err != nil {
		t.Fatal(err)
	}
	mounts, err := index.List()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mounts, []Mount{personal, work}) {
		t.Fatalf("mounts = %#v", mounts)
	}
	if err := index.Remove(personal); err != nil {
		t.Fatal(err)
	}
	mounts, err = index.List()
	if err != nil || !reflect.DeepEqual(mounts, []Mount{work}) {
		t.Fatalf("mounts after removal = %#v, %v", mounts, err)
	}
}
