package agentloop_test

import (
	"bytes"
	"strings"
	"testing"

	"midgard/internal/agentloop"
)

func TestTextActivitySinkPreservesHeadlessProgressFormat(t *testing.T) {
	var output bytes.Buffer
	sink := &agentloop.TextActivitySink{Writer: &output}
	sink.EmitActivity(agentloop.Activity{Kind: "tool", Name: "file_inspect", Arguments: `{"path":"README.md"}`})
	if got := output.String(); got != "[tool] file_inspect {\"path\":\"README.md\"}\n" {
		t.Fatalf("output = %q", got)
	}
	long := strings.Repeat("x", 5000)
	sink.EmitActivity(agentloop.Activity{Kind: "agent", Message: long})
	if output.Len() > 4300 || !strings.Contains(output.String(), "[truncated]") {
		t.Fatalf("long output was not bounded: %d bytes", output.Len())
	}
}
