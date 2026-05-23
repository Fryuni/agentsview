//go:build pgtest

package postgres

import (
	"context"
	"testing"

	"go.kenn.io/agentsview/internal/db"
)

// TestListSessions_HasSecret verifies that the HasSecret filter
// returns only sessions where secret_leak_count > 0.
func TestListSessions_HasSecret(t *testing.T) {
	pgURL := testPGURL(t)
	ensureStoreSchema(t, pgURL)

	store, err := NewStore(pgURL, testSchema, true)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	pg := store.DB()

	// Seed a session with leaks and one without.
	_, err = pg.Exec(`
		INSERT INTO sessions
			(id, machine, project, agent, first_message,
			 started_at, ended_at, message_count,
			 user_message_count, secret_leak_count)
		VALUES
			('has-secret-leaky', 'test-machine', 'test-project',
			 'claude-code', 'secret session',
			 '2026-03-12T09:00:00Z'::timestamptz,
			 '2026-03-12T09:30:00Z'::timestamptz,
			 2, 1, 3),
			('has-secret-clean', 'test-machine', 'test-project',
			 'claude-code', 'clean session',
			 '2026-03-12T08:00:00Z'::timestamptz,
			 '2026-03-12T08:30:00Z'::timestamptz,
			 2, 1, 0)
	`)
	if err != nil {
		t.Fatalf("inserting test sessions: %v", err)
	}

	ctx := context.Background()
	page, err := store.ListSessions(ctx, db.SessionFilter{
		HasSecret: true,
		Limit:     50,
	})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	// Only the leaky session should appear.
	for _, s := range page.Sessions {
		if s.ID == "has-secret-clean" {
			t.Errorf(
				"clean session (secret_leak_count=0) included in HasSecret results",
			)
		}
	}

	var found *db.Session
	for i := range page.Sessions {
		if page.Sessions[i].ID == "has-secret-leaky" {
			found = &page.Sessions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("leaky session not found in HasSecret results")
	}
	if found.SecretLeakCount != 3 {
		t.Errorf(
			"SecretLeakCount = %d, want 3",
			found.SecretLeakCount,
		)
	}
}
