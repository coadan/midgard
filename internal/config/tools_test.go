package config

import "testing"

func TestRequireToolsReportsMissingTool(t *testing.T) {
	err := RequireTools("definitely-not-a-midgard-tool")
	if err == nil {
		t.Fatal("missing tool was accepted")
	}
}
