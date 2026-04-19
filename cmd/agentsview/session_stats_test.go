package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wesm/agentsview/internal/db"
)

// TestPrintSessionStatsHuman_Populated exercises the happy path with
// every optional section present. It does not pin exact text — the
// golden-file test in Task 20 owns that — but it guards the sections
// and nil-pointer branches that are hardest to eyeball in the stub.
func TestPrintSessionStatsHuman_Populated(t *testing.T) {
	prsOpened := 12
	prsMerged := 9
	stats := &db.SessionStats{
		SchemaVersion: 1,
		Window: db.StatsWindow{
			Since: "2026-03-21T00:00:00Z",
			Until: "2026-04-18T00:00:00Z",
			Days:  28,
		},
		Filters: db.StatsFilters{
			Agent:            "all",
			ProjectsExcluded: []string{},
			Timezone:         "America/New_York",
		},
		Totals: db.StatsTotals{
			SessionsAll:        11905,
			SessionsHuman:      322,
			SessionsAutomation: 11583,
			MessagesTotal:      109324,
			UserMessagesTotal:  3012,
		},
		Archetypes: db.StatsArchetypes{
			Automation:   11583,
			Quick:        125,
			Standard:     101,
			Deep:         79,
			Marathon:     17,
			Primary:      "automation",
			PrimaryHuman: "quick",
		},
		Distributions: db.StatsDistributions{
			DurationMinutes: db.ScopedDistributionPair{
				ScopeAll:   db.ScopedDistribution{Mean: 14.7},
				ScopeHuman: db.ScopedDistribution{Mean: 22.0},
			},
			UserMessages: db.ScopedDistributionPair{
				ScopeAll:   db.ScopedDistribution{Mean: 11.2},
				ScopeHuman: db.ScopedDistribution{Mean: 7.2},
			},
			PeakContextTokens: db.PeakContextDistribution{
				ScopeAll:  db.ScopedDistribution{Mean: 48000},
				NullCount: 0,
			},
			ToolsPerTurn: db.ScopedDistributionPair{
				ScopeAll: db.ScopedDistribution{Mean: 2.3},
			},
		},
		Velocity: db.StatsVelocity{
			TurnCycleSeconds: db.StatsPercentiles{
				P50: 20, P90: 90, Mean: 45,
			},
			FirstResponseSeconds: db.StatsPercentiles{
				P50: 5, P90: 15, Mean: 8,
			},
			MessagesPerActiveHour: 120.0,
		},
		ToolMix: db.StatsToolMix{
			ByCategory: map[string]int{
				"Bash": 1234, "Edit": 876, "Read": 543,
				"Grep": 321, "Glob": 210, "Write": 50,
			},
			TotalCalls: 3234,
		},
		ModelMix: db.StatsModelMix{
			ByTokens: map[string]int64{
				"claude-opus-4-7":   5600000,
				"claude-sonnet-4-6": 1200000,
			},
		},
		AgentPortfolio: db.StatsAgentPortfolio{
			BySessions: map[string]int{"claude": 11905, "codex": 234},
			ByTokens:   map[string]int64{"claude": 6800000, "codex": 120000},
			ByMessages: map[string]int{"claude": 109000, "codex": 2100},
			Primary:    "claude",
		},
		CacheEconomics: &db.StatsCacheEconomics{
			ClaudeOnly: true,
			CacheHitRatio: db.CacheHitRatioDistribution{
				Overall: 0.78,
			},
			DollarsSavedVsUncached: 88.54,
			DollarsSpent:           42.13,
		},
		Adoption: &db.StatsAdoption{
			ClaudeOnly:          true,
			PlanModeRate:        0.12,
			SubagentsPerSession: 0.3,
			DistinctSkills:      8,
		},
		Temporal: db.StatsTemporal{
			HourlyUTC: []db.TemporalHourlyUTCEntry{
				{TS: "2026-04-01T00:00:00Z", Sessions: 3, UserMessages: 12},
				{TS: "2026-04-01T01:00:00Z", Sessions: 2, UserMessages: 8},
			},
			ReporterTimezone: "America/New_York",
		},
		OutcomeStats: &db.StatsOutcomeStats{
			ReposActive:  3,
			Commits:      84,
			LOCAdded:     5421,
			LOCRemoved:   1823,
			FilesChanged: 127,
			PRsOpened:    &prsOpened,
			PRsMerged:    &prsMerged,
		},
		Outcomes: &db.StatsOutcomes{
			ClaudeOnly:            true,
			Success:               280,
			Failure:               14,
			Unknown:               28,
			GradeDistribution:     map[string]int{"A": 120, "B": 95, "C": 52, "D": 13, "F": 0},
			ToolRetryRate:         0.064,
			CompactionsPerSession: 0.1,
			AvgEditChurn:          1.2,
		},
		GeneratedAt: "2026-04-18T00:00:00Z",
	}

	var buf bytes.Buffer
	if err := printSessionStatsHuman(&buf, stats); err != nil {
		t.Fatalf("printSessionStatsHuman: %v", err)
	}
	out := buf.String()
	if len(out) < 200 {
		t.Fatalf("output suspiciously short (%d bytes):\n%s", len(out), out)
	}

	// Guard every major section header so accidental drops are caught.
	wants := []string{
		"Session window:",
		"Totals",
		"Archetypes",
		"Session shape",
		"Velocity",
		"Tool mix",
		"Model mix",
		"Agent portfolio",
		"Cache economics",
		"Adoption",
		"Temporal",
		"Outcome stats",
		"Outcomes",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("missing section heading %q in output:\n%s", w, out)
		}
	}

	// Thousands separators must be applied to large counts.
	if !strings.Contains(out, "11,905") {
		t.Errorf("expected thousands separator for 11,905, got:\n%s", out)
	}
}

// TestPrintSessionStatsHuman_Empty guards the zero-session short
// circuit: no optional sections, just the header + "no sessions".
func TestPrintSessionStatsHuman_Empty(t *testing.T) {
	stats := &db.SessionStats{
		SchemaVersion: 1,
		Window: db.StatsWindow{
			Since: "2026-04-11T00:00:00Z",
			Until: "2026-04-18T00:00:00Z",
			Days:  7,
		},
		Filters: db.StatsFilters{
			Agent:            "all",
			ProjectsExcluded: []string{},
			Timezone:         "UTC",
		},
	}

	var buf bytes.Buffer
	if err := printSessionStatsHuman(&buf, stats); err != nil {
		t.Fatalf("printSessionStatsHuman: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no sessions") {
		t.Errorf("expected zero-session placeholder in output:\n%s", out)
	}
	// No optional section headers should appear.
	for _, banned := range []string{
		"Archetypes", "Velocity", "Cache economics", "Outcomes",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("section %q must not appear for empty window:\n%s",
				banned, out)
		}
	}
}

// TestFmtInt64 covers the thousands-separator helper.
func TestFmtInt64(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{5, "5"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
		{123456, "123,456"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, c := range cases {
		if got := fmtInt64(c.in); got != c.want {
			t.Errorf("fmtInt64(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
