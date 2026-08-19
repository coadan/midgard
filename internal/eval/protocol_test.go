package eval_test

import (
	"path/filepath"
	"testing"

	"midgard/internal/eval"
)

func TestBragiProtocolCorpus(t *testing.T) {
	manifest, err := eval.LoadManifest(filepath.Join("..", "..", "testdata", "protocol", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Score(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if report.Protocol != "bragi/1.0" || report.Passed != len(manifest.Cases) {
		t.Fatalf("report = %#v", report)
	}
}
