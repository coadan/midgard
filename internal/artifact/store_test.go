package artifact_test

import (
	"os"
	"strings"
	"testing"

	"midgard/internal/artifact"
)

func TestArtifactsAreContentAddressedAndVerified(t *testing.T) {
	store, err := artifact.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Put(strings.NewReader("provider-native trace"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(strings.NewReader("provider-native trace"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref != second.Ref || first.Path != second.Path {
		t.Fatalf("same content did not deduplicate: %#v %#v", first, second)
	}
	if err := store.Verify(first.Ref); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("artifact permissions = %o", info.Mode().Perm())
	}
}
