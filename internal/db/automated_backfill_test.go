package db

import (
	"context"
	"testing"
	"time"
)

func TestBackfillIsAutomatedBidirectional(t *testing.T) {
	d := testDB(t)

	// Seed a false negative: single-turn roborev session with
	// is_automated = 0 (simulates pre-migration data).
	insertSession(t, d, "missed", "proj", func(s *Session) {
		fm := "You are a code reviewer. Review the code."
		s.FirstMessage = &fm
		s.MessageCount = 3
		s.UserMessageCount = 1
	})
	// Force is_automated to 0 to simulate pre-migration state.
	_, err := d.getWriter().Exec(
		"UPDATE sessions SET is_automated = 0 WHERE id = 'missed'",
	)
	requireNoError(t, err, "force missed to 0")

	// Seed a stale false positive: multi-turn session that was
	// previously marked automated under old broad rules.
	insertSession(t, d, "stale", "proj", func(s *Session) {
		fm := "# Fix Request for login flow"
		s.FirstMessage = &fm
		s.MessageCount = 10
		s.UserMessageCount = 5
	})
	_, err = d.getWriter().Exec(
		"UPDATE sessions SET is_automated = 1 WHERE id = 'stale'",
	)
	requireNoError(t, err, "force stale to 1")

	// Clear the marker so the backfill will run.
	_, err = d.getWriter().Exec(
		"DELETE FROM stats WHERE key = ?",
		IsAutomatedBackfillMarker,
	)
	requireNoError(t, err, "clear marker")

	// Run backfill.
	d.mu.Lock()
	err = d.backfillIsAutomatedLocked(d.getWriter())
	d.mu.Unlock()
	requireNoError(t, err, "first backfill run")

	ctx := context.Background()

	// False negative should now be set.
	missed, err := d.GetSession(ctx, "missed")
	requireNoError(t, err, "get missed")
	if !missed.IsAutomated {
		t.Error("missed session should be automated after backfill")
	}

	// Stale false positive should now be cleared.
	stale, err := d.GetSession(ctx, "stale")
	requireNoError(t, err, "get stale")
	if stale.IsAutomated {
		t.Error("stale session should not be automated after backfill")
	}
}

func TestBackfillIsAutomatedMarkerIdempotent(t *testing.T) {
	d := testDB(t)

	// Seed a roborev session.
	insertSession(t, d, "review", "proj", func(s *Session) {
		fm := "You are a code reviewer. Review the code."
		s.FirstMessage = &fm
		s.MessageCount = 3
		s.UserMessageCount = 1
	})

	// Clear the marker and run backfill.
	_, err := d.getWriter().Exec(
		"DELETE FROM stats WHERE key = ?",
		IsAutomatedBackfillMarker,
	)
	requireNoError(t, err, "clear marker")

	d.mu.Lock()
	err = d.backfillIsAutomatedLocked(d.getWriter())
	d.mu.Unlock()
	requireNoError(t, err, "first run")

	// Manually corrupt the session to verify second run is a no-op.
	_, err = d.getWriter().Exec(
		"UPDATE sessions SET is_automated = 0 WHERE id = 'review'",
	)
	requireNoError(t, err, "corrupt")

	// Second run should be a no-op (marker present).
	d.mu.Lock()
	err = d.backfillIsAutomatedLocked(d.getWriter())
	d.mu.Unlock()
	requireNoError(t, err, "second run")

	ctx := context.Background()
	review, err := d.GetSession(ctx, "review")
	requireNoError(t, err, "get review")
	if review.IsAutomated {
		t.Error("second run should be no-op; is_automated should still be 0")
	}
}

func TestBackfillIsAutomatedBumpsLocalModifiedAt(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	// Seed a single-turn roborev session that the new classifier
	// will flip to is_automated = 1.
	insertSession(t, d, "to-flip", "proj", func(s *Session) {
		fm := "You are a code reviewer. Review the code."
		s.FirstMessage = &fm
		s.MessageCount = 3
		s.UserMessageCount = 1
	})
	// Force is_automated = 0 so the backfill has work to do.
	_, err := d.getWriter().Exec(
		"UPDATE sessions SET is_automated = 0 WHERE id = 'to-flip'",
	)
	requireNoError(t, err, "force to-flip to 0")

	// Snapshot local_modified_at before the backfill.
	before, err := d.GetSessionFull(ctx, "to-flip")
	requireNoError(t, err, "get to-flip before")
	var beforeLM string
	if before.LocalModifiedAt != nil {
		beforeLM = *before.LocalModifiedAt
	}

	// SQLite's strftime('now') ticks at millisecond precision.
	// Sleep a few ms so a re-set produces a strictly later value.
	// (Mirrors internal/db/signals_test.go:164.)
	time.Sleep(5 * time.Millisecond)

	// Clear the marker so the backfill runs.
	_, err = d.getWriter().Exec(
		"DELETE FROM stats WHERE key = ?",
		IsAutomatedBackfillMarker,
	)
	requireNoError(t, err, "clear marker")

	d.mu.Lock()
	err = d.backfillIsAutomatedLocked(d.getWriter())
	d.mu.Unlock()
	requireNoError(t, err, "backfill run")

	after, err := d.GetSessionFull(ctx, "to-flip")
	requireNoError(t, err, "get to-flip after")
	if !after.IsAutomated {
		t.Fatal("to-flip should be automated after backfill")
	}
	if after.LocalModifiedAt == nil || *after.LocalModifiedAt == "" {
		t.Fatal("local_modified_at not set after backfill")
	}
	if *after.LocalModifiedAt <= beforeLM {
		t.Errorf(
			"local_modified_at not bumped: before=%q after=%q",
			beforeLM, *after.LocalModifiedAt,
		)
	}
}
