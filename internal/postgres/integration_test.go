//go:build pgtest

package postgres

import (
	"context"
	"testing"
	"time"

	"go.kenn.io/agentsview/internal/db"
)

// TestPGPushSecrets verifies that pg push carries secret_leak_count
// and secret_findings rows to PostgreSQL. It also verifies idempotency
// (delete-before-insert prevents duplicates on re-push).
func TestPGPushSecrets(t *testing.T) {
	pgURL := testPGURL(t)
	cleanPGSchema(t, pgURL)
	t.Cleanup(func() { cleanPGSchema(t, pgURL) })

	local := testDB(t)
	ps, err := New(
		pgURL, "agentsview", local, "machine-secrets", true,
		SyncOptions{},
	)
	if err != nil {
		t.Fatalf("creating sync: %v", err)
	}
	defer ps.Close()

	ctx := context.Background()
	if err := ps.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	// Seed a session with a message.
	started := time.Now().UTC().Format(time.RFC3339)
	firstMsg := "push secrets test"
	sess := db.Session{
		ID:           "secrets-sess-001",
		Project:      "secrets-project",
		Machine:      "local",
		Agent:        "claude",
		FirstMessage: &firstMsg,
		StartedAt:    &started,
		MessageCount: 1,
	}
	if err := local.UpsertSession(sess); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if err := local.InsertMessages([]db.Message{{
		SessionID: "secrets-sess-001",
		Ordinal:   0,
		Role:      "user",
		Content:   firstMsg,
	}}); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	// Add a secret finding to the session.
	findings := []db.SecretFinding{
		{
			SessionID:      "secrets-sess-001",
			RuleName:       "aws-access-key",
			Confidence:     "definite",
			LocationKind:   "message",
			MessageOrdinal: 0,
			MatchStart:     5,
			MatchEnd:       25,
			MatchIndex:     0,
			RedactedMatch:  "AKIA[REDACTED]",
			RulesVersion:   "v1.0",
		},
	}
	if err := local.ReplaceSessionSecretFindings(
		"secrets-sess-001", findings, 1, "v1.0",
	); err != nil {
		t.Fatalf("replace secret findings: %v", err)
	}

	// Push to PG.
	pushResult, err := ps.Push(ctx, false, nil)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushResult.SessionsPushed != 1 {
		t.Fatalf(
			"pushed %d sessions; want 1",
			pushResult.SessionsPushed,
		)
	}

	// Open a read Store and verify via ListSessions + ListSecretFindings.
	store, err := NewStore(pgURL, "agentsview", true)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer store.Close()

	// ListSessions with HasSecret=true must return the session with
	// SecretLeakCount=1.
	page, err := store.ListSessions(ctx, db.SessionFilter{
		HasSecret: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSessions HasSecret: %v", err)
	}
	if len(page.Sessions) != 1 {
		t.Fatalf(
			"ListSessions HasSecret returned %d sessions; want 1",
			len(page.Sessions),
		)
	}
	if page.Sessions[0].SecretLeakCount != 1 {
		t.Errorf(
			"SecretLeakCount = %d; want 1",
			page.Sessions[0].SecretLeakCount,
		)
	}

	// ListSecretFindings must return the pushed finding with
	// Project and Agent populated.
	fpage, err := store.ListSecretFindings(ctx, db.SecretFindingFilter{
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSecretFindings: %v", err)
	}
	if len(fpage.Findings) != 1 {
		t.Fatalf(
			"ListSecretFindings returned %d findings; want 1",
			len(fpage.Findings),
		)
	}
	f := fpage.Findings[0]
	if f.RuleName != "aws-access-key" {
		t.Errorf("RuleName = %q; want aws-access-key", f.RuleName)
	}
	if f.Project != "secrets-project" {
		t.Errorf("Project = %q; want secrets-project", f.Project)
	}
	if f.Agent != "claude" {
		t.Errorf("Agent = %q; want claude", f.Agent)
	}
	if f.RedactedMatch != "AKIA[REDACTED]" {
		t.Errorf(
			"RedactedMatch = %q; want AKIA[REDACTED]",
			f.RedactedMatch,
		)
	}

	// Second push with no changes must be a no-op (fingerprint
	// unchanged) and must not duplicate findings.
	r2, err := ps.Push(ctx, false, nil)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if r2.SessionsPushed != 0 {
		t.Errorf(
			"second push sessions = %d; want 0 (no-op)",
			r2.SessionsPushed,
		)
	}
	fpage2, err := store.ListSecretFindings(ctx, db.SecretFindingFilter{
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSecretFindings after re-push: %v", err)
	}
	if len(fpage2.Findings) != 1 {
		t.Errorf(
			"findings after re-push = %d; want 1 (no duplicate)",
			len(fpage2.Findings),
		)
	}
}

// TestPushSecretFindingsReportsChange verifies pushSecretFindings reports
// whether it changed any rows, so the caller can bump sessions.updated_at
// for secret-only changes that pushSession and pushMessages would miss.
func TestPushSecretFindingsReportsChange(t *testing.T) {
	pgURL := testPGURL(t)
	cleanPGSchema(t, pgURL)
	t.Cleanup(func() { cleanPGSchema(t, pgURL) })

	local := testDB(t)
	ps, err := New(
		pgURL, "agentsview", local, "machine-findings-change", true,
		SyncOptions{},
	)
	if err != nil {
		t.Fatalf("creating sync: %v", err)
	}
	defer ps.Close()

	ctx := context.Background()
	if err := ps.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	// Seed a session (no findings yet) and push it so the PG row exists
	// for the secret_findings foreign key.
	started := time.Now().UTC().Format(time.RFC3339)
	firstMsg := "findings change test"
	const sessID = "findings-change-001"
	sess := db.Session{
		ID:           sessID,
		Project:      "findings-project",
		Machine:      "local",
		Agent:        "claude",
		FirstMessage: &firstMsg,
		StartedAt:    &started,
		MessageCount: 1,
	}
	if err := local.UpsertSession(sess); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if err := local.InsertMessages([]db.Message{{
		SessionID: sessID,
		Ordinal:   0,
		Role:      "user",
		Content:   firstMsg,
	}}); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if _, err := ps.Push(ctx, false, nil); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	// pushOnce runs pushSecretFindings in its own transaction and returns
	// whether it reported a change.
	pushOnce := func() bool {
		tx, err := ps.pg.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		changed, err := ps.pushSecretFindings(ctx, tx, sessID)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("pushSecretFindings: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit tx: %v", err)
		}
		return changed
	}

	finding := db.SecretFinding{
		SessionID:      sessID,
		RuleName:       "aws-access-key",
		Confidence:     "definite",
		LocationKind:   "message",
		MessageOrdinal: 0,
		MatchStart:     5,
		MatchEnd:       25,
		MatchIndex:     0,
		RedactedMatch:  "AKIA[REDACTED]",
		RulesVersion:   "v1.0",
	}

	// No PG rows, no local findings: nothing changes.
	if pushOnce() {
		t.Error("empty -> empty reported a change; want false")
	}

	// Local gains a finding: the insert is a change.
	if err := local.ReplaceSessionSecretFindings(
		sessID, []db.SecretFinding{finding}, 1, "v1.0",
	); err != nil {
		t.Fatalf("seed finding: %v", err)
	}
	if !pushOnce() {
		t.Error("insert reported no change; want true")
	}

	// Re-pushing the same finding still rewrites rows (delete + insert),
	// which counts as a change.
	if !pushOnce() {
		t.Error("rewrite reported no change; want true")
	}

	// Clearing local findings deletes the PG row: that is a change.
	if err := local.ReplaceSessionSecretFindings(
		sessID, nil, 0, "v1.0",
	); err != nil {
		t.Fatalf("clear findings: %v", err)
	}
	if !pushOnce() {
		t.Error("delete reported no change; want true")
	}

	// Back to empty on both sides: nothing changes.
	if pushOnce() {
		t.Error("post-clear empty -> empty reported a change; want false")
	}
}

func TestPGConnectivity(t *testing.T) {
	pgURL := testPGURL(t)

	local := testDB(t)
	ps, err := New(
		pgURL, "agentsview", local,
		"connectivity-test-machine", true,
		SyncOptions{},
	)
	if err != nil {
		t.Fatalf("creating sync: %v", err)
	}
	defer ps.Close()

	ctx, cancel := context.WithTimeout(
		context.Background(), 10*time.Second,
	)
	defer cancel()

	if err := ps.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	status, err := ps.Status(ctx)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}

	t.Logf("PG Sync Status: %+v", status)
}

func TestPGPushCycle(t *testing.T) {
	pgURL := testPGURL(t)

	cleanPGSchema(t, pgURL)
	t.Cleanup(func() { cleanPGSchema(t, pgURL) })

	local := testDB(t)
	ps, err := New(
		pgURL, "agentsview", local, "machine-a", true,
		SyncOptions{},
	)
	if err != nil {
		t.Fatalf("creating sync: %v", err)
	}
	defer ps.Close()

	ctx := context.Background()
	if err := ps.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	started := time.Now().UTC().Format(time.RFC3339)
	firstMsg := "hello from pg"
	sess := db.Session{
		ID:           "pg-sess-001",
		Project:      "pg-project",
		Machine:      "local",
		Agent:        "test-agent",
		FirstMessage: &firstMsg,
		StartedAt:    &started,
		MessageCount: 1,
	}
	if err := local.UpsertSession(sess); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if err := local.InsertMessages([]db.Message{{
		SessionID: "pg-sess-001",
		Ordinal:   0,
		Role:      "user",
		Content:   firstMsg,
	}}); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	pushResult, err := ps.Push(ctx, false, nil)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushResult.SessionsPushed != 1 ||
		pushResult.MessagesPushed != 1 {
		t.Fatalf(
			"pushed %d sessions, %d messages; want 1/1",
			pushResult.SessionsPushed,
			pushResult.MessagesPushed,
		)
	}

	status, err := ps.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.PGSessions != 1 {
		t.Errorf(
			"pg sessions = %d, want 1",
			status.PGSessions,
		)
	}
	if status.PGMessages != 1 {
		t.Errorf(
			"pg messages = %d, want 1",
			status.PGMessages,
		)
	}
}
