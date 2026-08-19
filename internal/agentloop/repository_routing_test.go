package agentloop

import (
	"encoding/json"
	"strings"
	"testing"

	"midgard/internal/workspace"
)

func TestRepositoryQualifiedToolsRouteToNamedBinding(t *testing.T) {
	coordinator := Coordinator{Runners: map[string]workspace.Runner{
		"bragi":   {Binding: workspace.Binding{RepositoryName: "bragi", WorktreeRoot: "/work/bragi"}},
		"midgard": {Binding: workspace.Binding{RepositoryName: "midgard", WorktreeRoot: "/work/midgard"}},
	}}
	runner, err := coordinator.runnerFor(json.RawMessage(`{"repository":"bragi","path":"README.md"}`))
	if err != nil || runner.Binding.WorktreeRoot != "/work/bragi" {
		t.Fatalf("routed runner = %#v, %v", runner.Binding, err)
	}
	if _, err := coordinator.runnerFor(json.RawMessage(`{"path":"README.md"}`)); err == nil || !strings.Contains(err.Error(), "bragi, midgard") {
		t.Fatalf("missing repository error = %v", err)
	}
}
