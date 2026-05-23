package main

import (
	"strings"
	"testing"
)

func TestSessionSearchFlagValidation(t *testing.T) {
	cmd := newSessionSearchCommand()
	cmd.SetArgs([]string{"needle", "--regex", "--fts"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestSessionSearchFTSWithToolSource(t *testing.T) {
	cmd := newSessionSearchCommand()
	cmd.SetArgs([]string{"needle", "--fts", "--in", "tool_result"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "messages only") {
		t.Fatalf("expected fts+tool rejection, got %v", err)
	}
}
