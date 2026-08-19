package agentloop

import (
	"encoding/json"
	"testing"
)

func TestAddToolGuidancePreservesStructuredFailure(t *testing.T) {
	raw := json.RawMessage(`{"stderr":"bad hunk","exit_code":128,"error_code":"patch_invalid"}`)
	guided := addToolGuidance(raw, "inspect and replace")
	var output map[string]any
	if err := json.Unmarshal(guided, &output); err != nil {
		t.Fatal(err)
	}
	if output["error_code"] != "patch_invalid" || output["midgard_guidance"] != "inspect and replace" {
		t.Fatalf("guided failure = %#v", output)
	}
}
