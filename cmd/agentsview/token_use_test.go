package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wesm/agentsview/internal/db"
	"github.com/wesm/agentsview/internal/parser"
)

// newTestDB opens a fresh SQLite DB in a temp dir for a single test.
func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// upsertSession inserts a session with minimal required fields.
func upsertSession(
	t *testing.T, d *db.DB, id, agent, startedAt string,
) {
	t.Helper()
	s := db.Session{
		ID:           id,
		Project:      "test-project",
		Machine:      "local",
		Agent:        agent,
		MessageCount: 1,
	}
	if startedAt != "" {
		s.StartedAt = &startedAt
	}
	if err := d.UpsertSession(s); err != nil {
		t.Fatalf("upsert %s: %v", id, err)
	}
}

func TestResolveSessionID_PrefixedInput_ReturnedUnchanged(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// Even when the DB has no matching row, a prefixed input
	// must be returned unchanged (exact-match contract).
	input := "codex:019d5490-fe31-7e62-838c-8ba4193f245d"
	got, known := resolveRawSessionID(ctx, d, nil, input)
	if got != input {
		t.Errorf("got %q, want %q", got, input)
	}
	if !known {
		t.Errorf("known = false, want true (prefixed input trusted)")
	}
}

func TestResolveSessionID_BareClaudeUUID_ExactMatch(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// Claude sessions have no prefix; the bare UUID is the
	// canonical ID stored in sessions.id.
	id := "11111111-1111-1111-1111-111111111111"
	upsertSession(t, d, id, "claude", "2026-04-17T10:00:00Z")

	got, known := resolveRawSessionID(ctx, d, nil, id)
	if got != id {
		t.Errorf("got %q, want %q", got, id)
	}
	if !known {
		t.Errorf("known = false, want true (DB match)")
	}
}

func TestResolveSessionID_BareCodexUUID_ResolvesToPrefixed(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	bare := "019d5490-fe31-7e62-838c-8ba4193f245d"
	stored := "codex:" + bare
	upsertSession(t, d, stored, "codex", "2026-04-17T10:00:00Z")

	got, known := resolveRawSessionID(ctx, d, nil, bare)
	if got != stored {
		t.Errorf("got %q, want %q", got, stored)
	}
	if !known {
		t.Errorf("known = false, want true (DB match)")
	}
}

func TestResolveSessionID_Ambiguous_MostRecentWins(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	bare := "22222222-2222-2222-2222-222222222222"
	// Older codex session.
	upsertSession(t, d, "codex:"+bare, "codex", "2026-04-16T10:00:00Z")
	// Newer amp session with same raw UUID.
	upsertSession(t, d, "amp:"+bare, "amp", "2026-04-17T10:00:00Z")

	got, known := resolveRawSessionID(ctx, d, nil, bare)
	if got != "amp:"+bare {
		t.Errorf("got %q, want amp:%s (most recent)", got, bare)
	}
	if !known {
		t.Errorf("known = false, want true")
	}
}

func TestResolveSessionID_NotInDB_FoundOnDisk(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// Create a codex session file on disk: the probe path
	// should resolve a bare raw UUID to the prefixed form.
	codexDir := filepath.Join(t.TempDir(), "codex-sessions")
	bare := "33333333-3333-3333-3333-333333333333"
	dayDir := filepath.Join(codexDir, "2026", "04", "17")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fname := "rollout-2026-04-17T10-00-00-" + bare + ".jsonl"
	fpath := filepath.Join(dayDir, fname)
	if err := os.WriteFile(fpath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	agentDirs := map[parser.AgentType][]string{
		parser.AgentCodex: {codexDir},
	}
	got, known := resolveRawSessionID(ctx, d, agentDirs, bare)
	if got != "codex:"+bare {
		t.Errorf("got %q, want codex:%s (disk probe)", got, bare)
	}
	if !known {
		t.Errorf("known = false, want true (disk probe found match)")
	}
}

func TestResolveSessionID_NotFoundAnywhere_PassThrough(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	bare := "44444444-4444-4444-4444-444444444444"
	got, known := resolveRawSessionID(ctx, d, nil, bare)
	if got != bare {
		t.Errorf("got %q, want %q (pass-through)", got, bare)
	}
	if known {
		t.Errorf("known = true, want false (nothing found)")
	}
}

func TestResolveSessionID_BareClaudeAndPrefixedSameUUID_ClaudeExactWins(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// Edge: a bare Claude UUID that ALSO exists as a prefixed
	// session (e.g. codex:<same-uuid>). The Claude row is an
	// exact match and should win over the suffix match.
	bare := "55555555-5555-5555-5555-555555555555"
	upsertSession(t, d, bare, "claude", "2026-04-16T10:00:00Z")
	upsertSession(t, d, "codex:"+bare, "codex", "2026-04-17T10:00:00Z")

	got, known := resolveRawSessionID(ctx, d, nil, bare)
	if got != bare {
		t.Errorf("got %q, want %q (exact claude match)", got, bare)
	}
	if !known {
		t.Errorf("known = false, want true")
	}
}

func TestTokenUseExitCode_Found(t *testing.T) {
	sess := &db.Session{
		ID:                   "codex:xxx",
		HasTotalOutputTokens: true,
		TotalOutputTokens:    100,
	}
	if got := tokenUseExitCode(sess); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestTokenUseExitCode_NoData(t *testing.T) {
	sess := &db.Session{ID: "codex:xxx"}
	if got := tokenUseExitCode(sess); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}

func TestTokenUseExitCode_NotFound(t *testing.T) {
	if got := tokenUseExitCode(nil); got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestTokenUseExitCode_PeakContextOnly(t *testing.T) {
	// Having only peak_context token data is still "has data".
	sess := &db.Session{
		ID:                   "claude:xxx",
		HasPeakContextTokens: true,
		PeakContextTokens:    50000,
	}
	if got := tokenUseExitCode(sess); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}
