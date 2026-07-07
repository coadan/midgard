package task

import "testing"

func TestObjectivePhraseReplacementPreservesCodeSpanBackticks(t *testing.T) {
	got, ok := objectivePhraseReplacement("change the phrase `UnmarshalTOML` TOML interface to the `UnmarshalTOML` interface.")
	if !ok {
		t.Fatal("replacement phrase not parsed")
	}
	if got.Old != "`UnmarshalTOML` TOML interface" || got.New != "the `UnmarshalTOML` interface" {
		t.Fatalf("replacement = %#v", got)
	}
}
