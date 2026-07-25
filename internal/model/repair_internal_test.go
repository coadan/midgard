package model

import (
	"fmt"
	"strings"
	"testing"
)

func TestCompactCommandLedgerKeepsAllRefsAndOnlyLatestDetail(t *testing.T) {
	turns := []commandTurn{
		{number: 1, text: "command id:cmd_1 repo:repo1 stdout:artifact:commands/cmd_1/stdout.txt\nstdout_preview:\nold detail\n"},
		{number: 2, text: "command id:cmd_2 repo:repo1 stdout:artifact:commands/cmd_2/stdout.txt\nstdout_preview:\nlatest detail\n"},
	}
	got := compactCommandLedger(turns)
	for _, want := range []string{"cmd_1", "cmd_2", "latest detail"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ledger missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old detail") {
		t.Fatalf("ledger retained obsolete detail:\n%s", got)
	}
}

func TestCompactCommandLedgerIsBounded(t *testing.T) {
	turns := make([]commandTurn, 0, 8)
	for i := 1; i <= 8; i++ {
		turns = append(turns, commandTurn{
			number: i,
			text: fmt.Sprintf(
				"command id:cmd_%d repo:repo1 stdout:artifact:commands/cmd_%d/stdout.txt\nstdout_preview:\n%s\n",
				i,
				i,
				strings.Repeat("x", 32<<10),
			),
		})
	}
	got := compactCommandLedger(turns)
	if len(got) > maxCommandLedgerBytes+256 {
		t.Fatalf("ledger = %d bytes, want bounded near %d", len(got), maxCommandLedgerBytes)
	}
	for i := 1; i <= 8; i++ {
		if !strings.Contains(got, fmt.Sprintf("cmd_%d", i)) {
			t.Fatalf("ledger lost command ref %d", i)
		}
	}
}
