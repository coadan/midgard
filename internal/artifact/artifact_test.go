package artifact

import (
	"testing"
)

func TestStorePutReadAndChecksum(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Put(Record{
		Path:  "scripts/edit.py",
		Type:  TypePayload,
		State: StateSealed,
	}, []byte("print('ok')\n"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Checksum == "" {
		t.Fatal("sealed artifact did not receive checksum")
	}
	if !record.CanExecute() {
		t.Fatal("sealed payload should be executable by policy checks")
	}
	data, err := store.Read("scripts/edit.py")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "print('ok')\n" {
		t.Fatalf("artifact data = %q", data)
	}
}

func TestValidatePathRejectsEscapes(t *testing.T) {
	for _, path := range []string{"../x", "scripts/../x.py", "/tmp/x.py", ".", ""} {
		t.Run(path, func(t *testing.T) {
			if err := ValidatePath(path); err == nil {
				t.Fatalf("ValidatePath(%q) succeeded, want error", path)
			}
		})
	}
}

func TestValidateReportPathForRole(t *testing.T) {
	if err := ValidateReportPath("implementer", "implementation.mdx"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReportPath("implementer", "plan.mdx"); err == nil {
		t.Fatal("planner report should not be accepted for implementer")
	}
}
