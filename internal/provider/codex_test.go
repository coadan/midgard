package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCodexBridgeRoutesRepositoryOperationsThroughMidgard(t *testing.T) {
	params := (&codexPreparedCall{model: "gpt-test"}).threadStartParams("/tmp/midgard-codex-test")
	if sandbox, _ := params["sandbox"].(string); sandbox != "danger-full-access" {
		t.Fatalf("sandbox = %q, want danger-full-access", sandbox)
	}
	if tools, _ := params["dynamicTools"].([]any); len(tools) != 0 {
		t.Fatalf("native dynamic tools = %#v, want none", tools)
	}
	instructions, _ := params["developerInstructions"].(string)
	for _, expected := range []string{"Do not decline repository work", "emit the corresponding Midgard protocol tool entity"} {
		if !strings.Contains(instructions, expected) {
			t.Fatalf("bridge instructions missing %q: %s", expected, instructions)
		}
	}
	if strings.Contains(instructions, "Do not call tools, inspect files, browse, or modify anything") {
		t.Fatalf("bridge instructions still prohibit protocol-routed work: %s", instructions)
	}
}

func TestCodexOutputCollectorUsesCompletedCommentaryAndIgnoresMirroredFinal(t *testing.T) {
	collector := newCodexOutputCollector()
	collector.Start(json.RawMessage(`{"item":{"id":"commentary","type":"agentMessage","phase":"commentary"}}`))
	if delta, accepted := collector.Delta(json.RawMessage(`{"itemId":"commentary","delta":"+ @inspect tool\n"}`)); !accepted || delta != "+ @inspect tool\n" {
		t.Fatalf("commentary delta = %q, accepted=%v", delta, accepted)
	}
	if !collector.CompletedCommentary(json.RawMessage(`{"item":{"id":"commentary","type":"agentMessage","phase":"commentary"}}`)) {
		t.Fatal("completed commentary did not establish a response boundary")
	}
	collector.Start(json.RawMessage(`{"item":{"id":"final","type":"agentMessage","phase":"final_answer"}}`))
	if delta, accepted := collector.Delta(json.RawMessage(`{"itemId":"final","delta":"+ @inspect tool\n"}`)); accepted || delta != "" {
		t.Fatalf("final delta = %q, accepted=%v", delta, accepted)
	}
	if got, want := collector.Content(), "+ @inspect tool\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestCodexOutputCollectorFallsBackForUnphasedServers(t *testing.T) {
	collector := newCodexOutputCollector()
	collector.Start(json.RawMessage(`{"item":{"id":"legacy","type":"agentMessage"}}`))
	if _, accepted := collector.Delta(json.RawMessage(`{"itemId":"legacy","delta":"final output"}`)); accepted {
		t.Fatal("unphased output streamed before compatibility fallback")
	}
	if got, want := collector.Content(), "final output"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestCodexCallWithinReturnsUsefulStartupTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	server := &codexServer{stdin: nopWriteCloser{Writer: io.Discard}, scan: bufio.NewScanner(reader)}
	defer reader.Close()
	defer writer.Close()

	_, err := server.callWithin(context.Background(), 10*time.Millisecond, 1, "initialize", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "did not respond to initialize within") {
		t.Fatalf("startup timeout = %v", err)
	}
}

func TestCodexWriteWithinStopsWhenAppServerStopsReading(t *testing.T) {
	reader, writer := io.Pipe()
	server := &codexServer{stdin: writer}
	defer reader.Close()
	defer writer.Close()

	err := server.writeWithin(context.Background(), 10*time.Millisecond, "model turn request", map[string]string{"input": strings.Repeat("x", 1024)})
	if err == nil || !strings.Contains(err.Error(), "did not accept the model turn request within") {
		t.Fatalf("write timeout = %v", err)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
