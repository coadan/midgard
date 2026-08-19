package protocol_test

import (
	"encoding/json"
	"testing"

	"midgard/internal/protocol"
)

func TestBragiCommitIsTheOnlyHostActionBoundary(t *testing.T) {
	turn, err := protocol.NewTurn()
	if err != nil {
		t.Fatal(err)
	}
	turn.Write("+ @t1 tool\n+ @t1.name \"shell\"\n+ @t1.arguments.command \"pwd\"\n+ @t1.reason \"inspect the repository\"\n")
	if actions, err := turn.HostActions(); err != nil || len(actions) != 0 {
		t.Fatalf("draft produced actions: %#v, %v", actions, err)
	}
	turn.Write("! @t1\n")
	actions, err := turn.HostActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Name != "shell" || actions[0].EntityID != "@t1" {
		t.Fatalf("unexpected actions: %#v", actions)
	}
	var arguments map[string]any
	if err := json.Unmarshal(actions[0].Arguments, &arguments); err != nil {
		t.Fatal(err)
	}
	if arguments["command"] != "pwd" {
		t.Fatalf("unexpected arguments: %#v", arguments)
	}
}

func TestMidgardProfileFingerprintIsPinned(t *testing.T) {
	turn, err := protocol.NewTurn()
	if err != nil {
		t.Fatal(err)
	}
	if got := turn.Negotiation().ProfileFingerprint; got != "sha256:3d7997c9ee4f9823d46f54370a92b1f62bef38470f6b30b9e7e3298003f315fb" {
		t.Fatalf("profile fingerprint = %s", got)
	}
}

func TestBragiRepairsDraftBeforeCommit(t *testing.T) {
	turn, err := protocol.NewTurn()
	if err != nil {
		t.Fatal(err)
	}
	updates := turn.Write("+ @t1 tool\n+ @t1.name \"shell\"\n+ @t1.arguments.command \"pw\"\n~ @t1.arguments.command \"pwd\"\n+ @t1.reason \"inspect\"\n! @t1\n")
	if len(updates) != 7 {
		t.Fatalf("got %d updates", len(updates))
	}
	actions, err := turn.HostActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || string(actions[0].Arguments) != `{"command":"pwd"}` {
		t.Fatalf("repair did not settle: %#v", actions)
	}
}

func TestBragiFinalRequiresCommittedRoutedMessage(t *testing.T) {
	turn, err := protocol.NewTurn()
	if err != nil {
		t.Fatal(err)
	}
	turn.Write("+ @m1 message\n+ @m1.speaker \"assistant\"\n+ @m1.audience \"user\"\n+ @m1.channel \"final\"\n+ @m1.content |\n|Done.\n! @m1.content\n")
	if got := turn.FinalMessages(); len(got) != 0 {
		t.Fatalf("draft leaked: %#v", got)
	}
	turn.Write("! @m1\n")
	if got := turn.FinalMessages(); len(got) != 1 || got[0] != "Done.\n" {
		t.Fatalf("unexpected final: %#v", got)
	}
}
