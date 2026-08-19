package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompanionPathsStayBesideMidgard(t *testing.T) {
	if got, want := YggdrasilPath("/opt/midgard/bin/midgard"), "/opt/midgard/bin/libexec/ygg"; got != want {
		t.Fatalf("YggdrasilPath() = %q, want %q", got, want)
	}
	if got, want := HeimdalPath("/opt/midgard/bin/midgard"), "/opt/midgard/bin/libexec/heimdal"; got != want {
		t.Fatalf("HeimdalPath() = %q, want %q", got, want)
	}
	if YggdrasilPath("") != "" || HeimdalPath("") != "" {
		t.Fatal("empty executable path resolved a companion")
	}
}

func TestResolveCompanionRequiresMatchingPinnedManifest(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(filepath.Join(bin, "libexec"), 0o700); err != nil {
		t.Fatal(err)
	}
	midgard := filepath.Join(bin, "midgard")
	ygg := YggdrasilPath(midgard)
	if err := os.WriteFile(ygg, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := digestFile(ygg)
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":"midgard.companion/v1","name":"ygg","module":"github.com/coadan/yggdrasil","version":"v0.3.0","sum":"h1:test","binary_sha256":"` + digest + `"}`
	if err := os.WriteFile(ygg+".manifest.json", []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveYggdrasil(midgard)
	if err != nil || resolved != ygg {
		t.Fatalf("ResolveYggdrasil() = %q, %v", resolved, err)
	}
	if err := os.WriteFile(ygg+".manifest.json", []byte(`{"schema":"midgard.companion/v1","name":"ygg","module":"github.com/coadan/yggdrasil","version":"v9","sum":"h1:test","binary_sha256":"`+digest+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveYggdrasil(midgard); err == nil {
		t.Fatal("mismatched companion manifest was accepted")
	}
	if err := os.WriteFile(ygg+".manifest.json", []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ygg, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveYggdrasil(midgard); err == nil {
		t.Fatal("companion with a mismatched digest was accepted")
	}
}
