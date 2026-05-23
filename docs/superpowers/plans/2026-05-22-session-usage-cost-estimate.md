# Session Usage Cost Estimate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a single-session cost estimate to agentsview via a new
`session usage <id>` command (deprecating `token-use`), and surface it in
roborev next to the existing token counts as `~$0.42`.

**Architecture:** A new `db.GetSessionUsage` aggregates per-session cost from
the existing pricing pipeline, starting from `GetSession` for metadata. A
`session usage` command reuses `token-use`'s direct-DB + on-demand-sync path
(adding non-destructive pricing seeding), and `token-use` becomes a deprecated
alias sharing the same core. roborev selects `session usage` vs `token-use` by
agentsview version and renders cost when available.

**Tech Stack:** Go, SQLite (`-tags fts5`, CGO), cobra; roborev: Go, cobra,
Bubble Tea.

**Spec:**
`docs/superpowers/specs/2026-05-22-session-usage-cost-estimate-design.md`

**Repos / working directories:**

- Phase A (agentsview):
  `/Users/wesm/.superset/worktrees/agentsview/feat/session-cost-estimate`
- Phase B (roborev): `/Users/wesm/code/roborev`

______________________________________________________________________

## Phase A — agentsview

### Task A1: Non-destructive `InsertMissingModelPricing`

A direct CLI helper that inserts fallback pricing rows only for model patterns
not already present, so it never clobbers richer LiteLLM/custom rows.

**Files:**

- Modify: `internal/db/pricing.go`

- Test: `internal/db/pricing_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/db/pricing_test.go`:

```go
func TestInsertMissingModelPricing_DoesNotOverwrite(t *testing.T) {
	d := testDB(t)

	// Seed an existing row (simulating a LiteLLM rate already present).
	if err := d.UpsertModelPricing([]ModelPricing{{
		ModelPattern: "claude-opus-4-6",
		InputPerMTok: 5.0, OutputPerMTok: 25.0,
		CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.5,
	}}); err != nil {
		t.Fatalf("UpsertModelPricing: %v", err)
	}

	// Insert-missing with a DIFFERENT rate for the same pattern, plus a
	// brand-new pattern.
	err := d.InsertMissingModelPricing([]ModelPricing{
		{ModelPattern: "claude-opus-4-6", InputPerMTok: 999.0, OutputPerMTok: 999.0},
		{ModelPattern: "gpt-5.4", InputPerMTok: 2.5, OutputPerMTok: 15.0},
	})
	requireNoError(t, err, "InsertMissingModelPricing")

	// Existing row is untouched.
	opus, err := d.GetModelPricing("claude-opus-4-6")
	requireNoError(t, err, "GetModelPricing opus")
	if opus == nil || opus.InputPerMTok != 5.0 {
		t.Fatalf("opus InputPerMTok = %v, want 5.0 (not overwritten)", opus)
	}
	// New row was inserted.
	gpt, err := d.GetModelPricing("gpt-5.4")
	requireNoError(t, err, "GetModelPricing gpt")
	if gpt == nil || gpt.InputPerMTok != 2.5 {
		t.Fatalf("gpt-5.4 InputPerMTok = %v, want 2.5 (inserted)", gpt)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestInsertMissingModelPricing_DoesNotOverwrite -v`
Expected: FAIL — `d.InsertMissingModelPricing undefined`.

- [ ] **Step 3: Implement `InsertMissingModelPricing`**

Add to `internal/db/pricing.go` (after `UpsertModelPricing`):

```go
// InsertMissingModelPricing inserts pricing rows only for model
// patterns not already present, leaving existing rows untouched.
// Used by the direct CLI usage path to guarantee fallback rates
// exist without clobbering richer LiteLLM rows a running server may
// have written. Unlike UpsertModelPricing (ON CONFLICT DO UPDATE),
// this is non-destructive (ON CONFLICT DO NOTHING).
func (db *DB) InsertMissingModelPricing(
	prices []ModelPricing,
) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	tx, err := db.getWriter().Begin()
	if err != nil {
		return fmt.Errorf("beginning pricing insert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO model_pricing
			(model_pattern, input_per_mtok, output_per_mtok,
			 cache_creation_per_mtok, cache_read_per_mtok,
			 updated_at)
		VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(model_pattern) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("preparing pricing insert: %w", err)
	}
	defer stmt.Close()

	for _, p := range prices {
		if _, err := stmt.Exec(
			p.ModelPattern,
			p.InputPerMTok,
			p.OutputPerMTok,
			p.CacheCreationPerMTok,
			p.CacheReadPerMTok,
		); err != nil {
			return fmt.Errorf(
				"inserting pricing %q: %w", p.ModelPattern, err,
			)
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestInsertMissingModelPricing_DoesNotOverwrite -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/pricing.go internal/db/pricing_test.go
git commit -m "feat(db): add non-destructive InsertMissingModelPricing"
```

______________________________________________________________________

### Task A2: `SessionUsage` type and `GetSessionUsage`

The per-session token + cost aggregation. Starts from `GetSession` (so metadata
and session-level token aggregates survive even when there are no per-message
usage rows), then sums cost over the session's own usage rows with explicit
pricing lookups.

**Files:**

- Modify: `internal/db/usage.go`

- Test: `internal/db/usage_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/db/usage_test.go`:

```go
func seedOpusPricing(t *testing.T, d *DB) {
	t.Helper()
	if err := d.UpsertModelPricing([]ModelPricing{{
		ModelPattern: "claude-opus-4-6",
		InputPerMTok: 5.0, OutputPerMTok: 25.0,
		CacheCreationPerMTok: 6.25, CacheReadPerMTok: 0.5,
	}}); err != nil {
		t.Fatalf("UpsertModelPricing: %v", err)
	}
}

func TestGetSessionUsage_PricedModel(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	seedOpusPricing(t, d)

	insertSession(t, d, "claude:s1", "proj", func(s *Session) {
		s.Agent = "claude-code"
		s.StartedAt = new("2026-05-20T10:00:00Z")
		s.TotalOutputTokens = 500
		s.PeakContextTokens = 1200
		s.HasTotalOutputTokens = true
		s.HasPeakContextTokens = true
	})
	insertMessages(t, d, Message{
		SessionID: "claude:s1", Ordinal: 0, Role: "assistant",
		Timestamp: "2026-05-20T10:30:00Z", Model: "claude-opus-4-6",
		TokenUsage: json.RawMessage(
			`{"input_tokens":1000,"output_tokens":500}`),
	})

	u, err := d.GetSessionUsage(ctx, "claude:s1")
	requireNoError(t, err, "GetSessionUsage")
	if u == nil {
		t.Fatal("usage is nil")
	}
	if !u.HasCost {
		t.Fatal("HasCost = false, want true")
	}
	// 1000*5/1e6 + 500*25/1e6 = 0.005 + 0.0125 = 0.0175
	if math.Abs(u.CostUSD-0.0175) > 1e-9 {
		t.Errorf("CostUSD = %v, want 0.0175", u.CostUSD)
	}
	if u.TotalOutputTokens != 500 || u.PeakContextTokens != 1200 {
		t.Errorf("token fields = %d/%d, want 500/1200",
			u.TotalOutputTokens, u.PeakContextTokens)
	}
	if len(u.Models) != 1 || u.Models[0] != "claude-opus-4-6" {
		t.Errorf("Models = %v, want [claude-opus-4-6]", u.Models)
	}
	if len(u.UnpricedModels) != 0 {
		t.Errorf("UnpricedModels = %v, want empty", u.UnpricedModels)
	}
}

func TestGetSessionUsage_UnpricedModel(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	// No pricing seeded for this model.
	insertSession(t, d, "claude:s2", "proj", func(s *Session) {
		s.Agent = "claude-code"
		s.StartedAt = new("2026-05-20T10:00:00Z")
		s.TotalOutputTokens = 500
		s.HasTotalOutputTokens = true
	})
	insertMessages(t, d, Message{
		SessionID: "claude:s2", Ordinal: 0, Role: "assistant",
		Timestamp: "2026-05-20T10:30:00Z", Model: "local-llama-99",
		TokenUsage: json.RawMessage(
			`{"input_tokens":1000,"output_tokens":500}`),
	})

	u, err := d.GetSessionUsage(ctx, "claude:s2")
	requireNoError(t, err, "GetSessionUsage")
	if u.HasCost {
		t.Error("HasCost = true, want false (unpriced)")
	}
	if u.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 (partial suppressed)", u.CostUSD)
	}
	if len(u.UnpricedModels) != 1 || u.UnpricedModels[0] != "local-llama-99" {
		t.Errorf("UnpricedModels = %v, want [local-llama-99]", u.UnpricedModels)
	}
}

func TestGetSessionUsage_MixedPricedUnpriced(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	seedOpusPricing(t, d)
	insertSession(t, d, "claude:s3", "proj", func(s *Session) {
		s.Agent = "claude-code"
		s.StartedAt = new("2026-05-20T10:00:00Z")
	})
	insertMessages(t, d,
		Message{
			SessionID: "claude:s3", Ordinal: 0, Role: "assistant",
			Timestamp: "2026-05-20T10:30:00Z", Model: "claude-opus-4-6",
			TokenUsage: json.RawMessage(
				`{"input_tokens":1000,"output_tokens":500}`),
		},
		Message{
			SessionID: "claude:s3", Ordinal: 1, Role: "assistant",
			Timestamp: "2026-05-20T10:31:00Z", Model: "local-llama-99",
			TokenUsage: json.RawMessage(
				`{"input_tokens":1000,"output_tokens":500}`),
		},
	)

	u, err := d.GetSessionUsage(ctx, "claude:s3")
	requireNoError(t, err, "GetSessionUsage")
	if u.HasCost {
		t.Error("HasCost = true, want false (mixed)")
	}
	if u.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 (partial suppressed)", u.CostUSD)
	}
	if len(u.UnpricedModels) != 1 || u.UnpricedModels[0] != "local-llama-99" {
		t.Errorf("UnpricedModels = %v, want [local-llama-99]", u.UnpricedModels)
	}
}

func TestGetSessionUsage_ExplicitCostOnly(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	// Session with no per-message token rows; cost from usage_events.
	insertSession(t, d, "hermes:s4", "proj", func(s *Session) {
		s.Agent = "hermes"
		s.StartedAt = new("2026-05-20T10:00:00Z")
	})
	cost := 0.02
	if err := d.ReplaceSessionUsageEvents("hermes:s4", []UsageEvent{{
		SessionID: "hermes:s4", Source: "session", Model: "gpt-5.4",
		InputTokens: 100, OutputTokens: 50,
		CostUSD: &cost, CostStatus: "estimated", CostSource: "hermes",
		OccurredAt: "2026-05-20T10:05:00Z", DedupKey: "session:hermes:s4",
	}}); err != nil {
		t.Fatalf("ReplaceSessionUsageEvents: %v", err)
	}

	u, err := d.GetSessionUsage(ctx, "hermes:s4")
	requireNoError(t, err, "GetSessionUsage")
	if !u.HasCost {
		t.Error("HasCost = false, want true (explicit cost)")
	}
	if math.Abs(u.CostUSD-0.02) > 1e-9 {
		t.Errorf("CostUSD = %v, want 0.02", u.CostUSD)
	}
}

func TestGetSessionUsage_NoTokenRowsKeepsMetadata(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	insertSession(t, d, "claude:s5", "proj", func(s *Session) {
		s.Agent = "claude-code"
		s.StartedAt = new("2026-05-20T10:00:00Z")
		s.TotalOutputTokens = 700
		s.PeakContextTokens = 3000
		s.HasTotalOutputTokens = true
		s.HasPeakContextTokens = true
	})

	u, err := d.GetSessionUsage(ctx, "claude:s5")
	requireNoError(t, err, "GetSessionUsage")
	if u == nil {
		t.Fatal("usage is nil")
	}
	if u.TotalOutputTokens != 700 || u.PeakContextTokens != 3000 {
		t.Errorf("tokens = %d/%d, want 700/3000",
			u.TotalOutputTokens, u.PeakContextTokens)
	}
	if !u.HasTokenData {
		t.Error("HasTokenData = false, want true")
	}
	if u.HasCost {
		t.Error("HasCost = true, want false (no cost rows)")
	}
	if u.Models == nil {
		t.Error("Models = nil, want non-nil empty slice")
	}
}

func TestGetSessionUsage_NotFound(t *testing.T) {
	d := testDB(t)
	u, err := d.GetSessionUsage(context.Background(), "nope:x")
	requireNoError(t, err, "GetSessionUsage")
	if u != nil {
		t.Errorf("usage = %v, want nil", u)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestGetSessionUsage -v`
Expected: FAIL — `d.GetSessionUsage undefined` / `SessionUsage` not defined.

- [ ] **Step 3: Implement `SessionUsage`, `sessionRowCost`, `GetSessionUsage`**

Add to `internal/db/usage.go` (after `GetTopSessionsByCost`):

```go
// SessionUsage is the per-session token + cost summary returned by
// the `session usage` command. Cost is an estimate from the
// model_pricing catalog unless an agent reported cost directly
// (usage_events.cost_usd). CostUSD is non-zero only when HasCost is
// true; a partial total (some models unpriced) is never emitted.
type SessionUsage struct {
	SessionID         string   `json:"session_id"`
	Agent             string   `json:"agent"`
	Project           string   `json:"project"`
	TotalOutputTokens int      `json:"total_output_tokens"`
	PeakContextTokens int      `json:"peak_context_tokens"`
	HasTokenData      bool     `json:"has_token_data"`
	CostUSD           float64  `json:"cost_usd"`
	HasCost           bool     `json:"has_cost"`
	Models            []string `json:"models"`
	UnpricedModels    []string `json:"unpriced_models,omitempty"`
}

// sessionRowCost computes one usage row's cost and reports whether
// it was priced and whether it contributes to the estimate. A row
// contributes when it carries an explicit cost or any tokens.
// Unlike usageAmounts (which zero-fills missing pricing), this does
// an explicit map lookup so callers can distinguish "unpriced" from
// "$0".
func sessionRowCost(
	r usageScanRow, pricing map[string]modelRates,
) (cost float64, priced, contributes bool) {
	var inTok, outTok, crTok, rdTok int
	if r.usageSource == "message" {
		usage := gjson.Parse(r.tokenJSON)
		inTok = int(usage.Get("input_tokens").Int())
		outTok = int(usage.Get("output_tokens").Int())
		crTok = int(usage.Get("cache_creation_input_tokens").Int())
		rdTok = int(usage.Get("cache_read_input_tokens").Int())
	} else {
		inTok = r.inputTokens
		outTok = r.outputTokens
		crTok = r.cacheCreationInputTokens
		rdTok = r.cacheReadInputTokens
	}

	if r.costUSD.Valid {
		return r.costUSD.Float64, true, true
	}
	if inTok == 0 && outTok == 0 && crTok == 0 && rdTok == 0 {
		return 0, true, false
	}
	rates, ok := pricing[r.model]
	if !ok {
		return 0, false, true
	}
	cost = (float64(inTok)*rates.input +
		float64(outTok)*rates.output +
		float64(crTok)*rates.cacheCreation +
		float64(rdTok)*rates.cacheRead) / 1_000_000
	return cost, true, true
}

// GetSessionUsage returns one session's token totals and cost
// estimate. It starts from GetSession (so metadata and session-level
// token aggregates are reported even when there are no per-message
// usage rows), then aggregates cost over the session's own usage
// rows. Dedup is intra-session only; this reports the session's own
// usage, which can diverge from the dashboard's cross-session
// credited total for fork/subagent sessions. Returns (nil, nil) when
// the session does not exist.
func (db *DB) GetSessionUsage(
	ctx context.Context, sessionID string,
) (*SessionUsage, error) {
	sess, err := db.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}

	pricing, err := db.loadPricingMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading pricing: %w", err)
	}

	query := usageRowSelect() + ` AND u.session_id = ?
		ORDER BY u.ts ASC, u.session_id ASC,
		COALESCE(u.message_ordinal, -1) ASC`
	rows, err := db.getReader().QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("querying session usage: %w", err)
	}
	defer rows.Close()

	var cost float64
	contributing := false
	allPriced := true
	modelsSet := make(map[string]struct{})
	unpricedSet := make(map[string]struct{})

	type dedupKey struct{ msgID, reqID string }
	seen := make(map[dedupKey]struct{})

	for rows.Next() {
		r, scanErr := scanUsageRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scanning session usage row: %w", scanErr)
		}
		if r.claudeMessageID != "" && r.claudeRequestID != "" {
			key := dedupKey{r.claudeMessageID, r.claudeRequestID}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		} else if r.usageDedupKey != "" {
			key := dedupKey{"usage", r.usageDedupKey}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		}

		c, priced, contributes := sessionRowCost(r, pricing)
		if !contributes {
			continue
		}
		contributing = true
		modelsSet[r.model] = struct{}{}
		if priced {
			cost += c
		} else {
			allPriced = false
			unpricedSet[r.model] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating session usage rows: %w", err)
	}

	out := &SessionUsage{
		SessionID:         sess.ID,
		Agent:             sess.Agent,
		Project:           sess.Project,
		TotalOutputTokens: sess.TotalOutputTokens,
		PeakContextTokens: sess.PeakContextTokens,
		HasTokenData:      sess.HasTotalOutputTokens || sess.HasPeakContextTokens,
		Models:            sortedSetKeys(modelsSet),
		HasCost:           contributing && allPriced,
	}
	if out.HasCost {
		out.CostUSD = cost
	}
	if len(unpricedSet) > 0 {
		out.UnpricedModels = sortedSetKeys(unpricedSet)
	}
	return out, nil
}

// sortedSetKeys returns the map keys sorted; never nil so JSON
// renders "[]" rather than "null".
func sortedSetKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestGetSessionUsage -v`
Expected: PASS (all six).

- [ ] **Step 5: Run the full db package tests + vet**

Run: `CGO_ENABLED=1 go test -tags fts5 ./internal/db/ && go vet ./internal/db/`
Expected: PASS (confirms no regression in existing usage tests; `sort`/`gjson`
already imported in usage.go).

- [ ] **Step 6: Commit**

```bash
git add internal/db/usage.go internal/db/usage_test.go
git commit -m "feat(db): add GetSessionUsage for single-session cost estimate"
```

______________________________________________________________________

### Task A3: Shared usage core + deprecate `token-use`

Extract the resolve/sync/output logic into one shared function with cost-aware
output and pricing setup. `token-use` becomes a thin deprecated wrapper.

**Files:**

- Modify: `cmd/agentsview/token_use.go`

- Modify: `cmd/agentsview/usage.go` (add `insertMissingPricing` helper)

- Test: `cmd/agentsview/token_use_test.go`

- [ ] **Step 1: Add the `insertMissingPricing` helper**

In `cmd/agentsview/usage.go`, after `upsertPricing`, add:

```go
// insertMissingPricing inserts fallback rows for models not already
// priced, without overwriting existing rows. Used by the direct
// usage path so a CLI-only data dir still prices fallback-catalog
// models, while never clobbering richer LiteLLM rows.
func insertMissingPricing(
	database *db.DB, prices []pricing.ModelPricing,
) error {
	dbPrices := make([]db.ModelPricing, len(prices))
	for i, p := range prices {
		dbPrices[i] = db.ModelPricing{
			ModelPattern:         p.ModelPattern,
			InputPerMTok:         p.InputPerMTok,
			OutputPerMTok:        p.OutputPerMTok,
			CacheCreationPerMTok: p.CacheCreationPerMTok,
			CacheReadPerMTok:     p.CacheReadPerMTok,
		}
	}
	return database.InsertMissingModelPricing(dbPrices)
}
```

- [ ] **Step 2: Write the failing test (cost-aware exit code)**

In `cmd/agentsview/token_use_test.go`, replace the four `TestTokenUseExitCode_*`
tests (they currently build `*db.Session`) with `SessionUsage`-based
equivalents:

```go
func TestUsageExitCode_TokenData(t *testing.T) {
	u := &db.SessionUsage{HasTokenData: true}
	if got := usageExitCode(u); got != tokenUseExitOK {
		t.Errorf("got %d, want %d", got, tokenUseExitOK)
	}
}

func TestUsageExitCode_CostOnly(t *testing.T) {
	u := &db.SessionUsage{HasTokenData: false, HasCost: true}
	if got := usageExitCode(u); got != tokenUseExitOK {
		t.Errorf("got %d, want %d (cost-only must not be exit 3)",
			got, tokenUseExitOK)
	}
}

func TestUsageExitCode_NoData(t *testing.T) {
	u := &db.SessionUsage{}
	if got := usageExitCode(u); got != tokenUseExitNoTokenData {
		t.Errorf("got %d, want %d", got, tokenUseExitNoTokenData)
	}
}

func TestUsageExitCode_NotFound(t *testing.T) {
	if got := usageExitCode(nil); got != tokenUseExitNotFound {
		t.Errorf("got %d, want %d", got, tokenUseExitNotFound)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/agentsview/ -run TestUsageExitCode -v`
Expected: FAIL — `usageExitCode undefined`.

- [ ] **Step 4: Refactor `token_use.go` core**

In `cmd/agentsview/token_use.go`:

(a) Replace the `tokenUseExitCode` function with `usageExitCode`:

```go
// usageExitCode classifies a SessionUsage into an exit code: 2 when
// the session is not in the DB, 0 when token data OR cost is present,
// 3 when the session exists but has neither. Cost-only sessions
// (e.g. Hermes) return 0 so callers do not discard useful cost.
func usageExitCode(u *db.SessionUsage) int {
	if u == nil {
		return tokenUseExitNotFound
	}
	if u.HasTokenData || u.HasCost {
		return tokenUseExitOK
	}
	return tokenUseExitNoTokenData
}
```

(b) Replace the `tokenUseOutput` struct with the shared cost-aware output:

```go
// sessionUsageOutput is the JSON shape emitted by `session usage`
// and the deprecated `token-use`. It is a strict superset of the
// historical token-use output (same fields, plus cost). The shape
// is experimental and may change.
type sessionUsageOutput struct {
	db.SessionUsage
	ServerRunning bool `json:"server_running"`
}
```

(c) Change the signature of `tokenUse` to `sessionUsageData` and return the
output struct + exit code. Keep the entire body from the start of the function
through the on-demand sync block **verbatim** (config load, `MkdirAll`,
`serverActive` detection, startup-wait, `db.Open`, cursor secret,
`resolveRawSessionID`, the second server re-check, and the
`engine.SyncSingleSession` block). Change only: the function header, the pricing
setup inserted right after `defer database.Close()`, and the tail that builds
output. The new header and tail:

```go
func sessionUsageData(
	sessionID string,
) (*sessionUsageOutput, int, error) {
	appCfg, err := config.LoadMinimal()
	if err != nil {
		return nil, tokenUseExitErr, fmt.Errorf("loading config: %w", err)
	}
	// ... (verbatim body up to and including `defer database.Close()`) ...
```

Immediately after the existing cursor-secret block (right before
`ctx := context.Background()`), insert pricing setup (`serverActive` and
`appCfg` are already in scope from the verbatim body above):

```go
	// Pricing setup for the direct path: db.Open (unlike openDB)
	// neither applies custom rates nor seeds model_pricing. Custom
	// rates are in-memory only (safe always). Fallback seeding is a
	// DB write, so do it only when no writable local daemon owns the
	// DB (same condition as the on-demand sync below); a running
	// server already seeds pricing at startup.
	applyCustomPricing(database, appCfg)
	if !serverActive {
		if perr := insertMissingPricing(
			database, pricing.FallbackPricing(),
		); perr != nil {
			fmt.Fprintf(os.Stderr,
				"warning: pricing seed failed: %v\n", perr)
		}
	}
```

Then replace the tail (from `sess, err := database.GetSession(...)` to the end
of the function) with:

```go
	u, err := database.GetSessionUsage(ctx, resolvedID)
	if err != nil {
		return nil, tokenUseExitErr,
			fmt.Errorf("querying session usage: %w", err)
	}
	if u == nil {
		fmt.Fprintf(os.Stderr, "session not found: %s\n", sessionID)
		return nil, tokenUseExitNotFound, nil
	}
	if u.Agent == "" {
		if def, ok := parser.AgentByPrefix(u.SessionID); ok {
			u.Agent = string(def.Type)
		}
	}
	return &sessionUsageOutput{
		SessionUsage:  *u,
		ServerRunning: serverActive,
	}, usageExitCode(u), nil
}
```

(d) Replace `runTokenUse` with a deprecated wrapper that delegates and emits the
notice:

```go
func runTokenUse(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr,
			"usage: agentsview token-use <session-id>")
		os.Exit(tokenUseExitErr)
	}
	fmt.Fprintln(os.Stderr,
		"note: 'token-use' is deprecated; use 'session usage <id>' instead")

	out, code, err := sessionUsageData(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(tokenUseExitErr)
	}
	if out != nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(out); encErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", encErr)
			os.Exit(tokenUseExitErr)
		}
	}
	os.Exit(code)
}
```

Add `"go.kenn.io/agentsview/internal/pricing"` to the imports if not present.
Remove the now-unused `time` import only if nothing else uses it (the
startup-wait code still references `time`, so leave it).

- [ ] **Step 5: Run tests + vet to verify pass and no dead code**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/agentsview/ -run 'TestUsageExitCode|TestResolveSessionID' -v && go vet ./cmd/agentsview/`
Expected: PASS; vet clean (no unused `tokenUseOutput`/`tokenUseExitCode`).

- [ ] **Step 6: Commit**

```bash
git add cmd/agentsview/token_use.go cmd/agentsview/usage.go cmd/agentsview/token_use_test.go
git commit -m "refactor(cli): share usage core, add cost + pricing seed, deprecate token-use"
```

______________________________________________________________________

### Task A4: `session usage <id>` command

Register the new command on the `session` group; render JSON or human.

**Files:**

- Create: `cmd/agentsview/session_usage.go`

- Modify: `cmd/agentsview/session.go:34-40` (register the command)

- Test: `cmd/agentsview/session_usage_test.go`

- [ ] **Step 1: Write the failing test (human render)**

Create `cmd/agentsview/session_usage_test.go`:

```go
package main

import (
	"strings"
	"testing"

	"go.kenn.io/agentsview/internal/db"
)

func TestRenderSessionUsageHuman_WithCost(t *testing.T) {
	out := &sessionUsageOutput{
		SessionUsage: db.SessionUsage{
			SessionID: "claude:s1", Agent: "claude-code", Project: "proj",
			TotalOutputTokens: 28800, PeakContextTokens: 118000,
			HasTokenData: true, CostUSD: 0.42, HasCost: true,
			Models: []string{"claude-opus-4-6"},
		},
	}
	var b strings.Builder
	if err := renderSessionUsageHuman(&b, out); err != nil {
		t.Fatalf("render: %v", err)
	}
	s := b.String()
	if !strings.Contains(s, "~$0.42") {
		t.Errorf("output missing cost:\n%s", s)
	}
	if !strings.Contains(s, "claude-opus-4-6") {
		t.Errorf("output missing model:\n%s", s)
	}
}

func TestRenderSessionUsageHuman_NoCost(t *testing.T) {
	out := &sessionUsageOutput{
		SessionUsage: db.SessionUsage{
			SessionID: "claude:s2", Agent: "claude-code",
			HasTokenData: true, HasCost: false,
			UnpricedModels: []string{"local-llama-99"},
		},
	}
	var b strings.Builder
	if err := renderSessionUsageHuman(&b, out); err != nil {
		t.Fatalf("render: %v", err)
	}
	s := b.String()
	if strings.Contains(s, "$") {
		t.Errorf("no-cost output should not contain '$':\n%s", s)
	}
	if !strings.Contains(s, "local-llama-99") {
		t.Errorf("output should note unpriced model:\n%s", s)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/agentsview/ -run TestRenderSessionUsageHuman -v`
Expected: FAIL — `renderSessionUsageHuman undefined`.

- [ ] **Step 3: Implement the command + renderer**

Create `cmd/agentsview/session_usage.go`:

```go
// ABOUTME: `session usage <id>` subcommand — prints per-session
// ABOUTME: token statistics and a cost estimate (JSON or human).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newSessionUsageCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "usage <id>",
		Short:        "Show token usage and cost estimate for a session",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			runSessionUsage(args[0], outputFormat(cmd))
		},
	}
}

// runSessionUsage computes usage for one session and renders it,
// exiting with the shared usage exit code (0 = token data or cost,
// 2 = not found, 3 = neither). Uses Run + os.Exit (not RunE) so the
// 2/3 codes survive — cobra RunE errors collapse to exit 1.
func runSessionUsage(sessionID, format string) {
	out, code, err := sessionUsageData(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(tokenUseExitErr)
	}
	if out != nil {
		if format == "json" {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(out); encErr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", encErr)
				os.Exit(tokenUseExitErr)
			}
		} else if rerr := renderSessionUsageHuman(
			os.Stdout, out,
		); rerr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", rerr)
			os.Exit(tokenUseExitErr)
		}
	}
	os.Exit(code)
}

// renderSessionUsageHuman writes a compact key/value summary. The
// cost line shows "~$X.XX (models)" when a complete estimate exists,
// otherwise "n/a" (noting any unpriced models). The tilde marks the
// figure as a model-pricing estimate.
func renderSessionUsageHuman(w io.Writer, out *sessionUsageOutput) error {
	label := func(name string) string {
		return fmt.Sprintf("%-12s", name+":")
	}
	fmt.Fprintf(w, "%s %s\n", label("Session"),
		sanitizeTerminal(out.SessionID))
	fmt.Fprintf(w, "%s %s\n", label("Agent"),
		sanitizeTerminal(out.Agent))
	fmt.Fprintf(w, "%s %d\n", label("Output"), out.TotalOutputTokens)
	fmt.Fprintf(w, "%s %d\n", label("Peak ctx"), out.PeakContextTokens)
	if out.HasCost {
		models := strings.Join(out.Models, ", ")
		fmt.Fprintf(w, "%s ~$%.2f (%s)\n", label("Cost"),
			out.CostUSD, sanitizeTerminal(models))
	} else if len(out.UnpricedModels) > 0 {
		fmt.Fprintf(w, "%s n/a (unpriced: %s)\n", label("Cost"),
			sanitizeTerminal(strings.Join(out.UnpricedModels, ", ")))
	} else {
		fmt.Fprintf(w, "%s n/a\n", label("Cost"))
	}
	return nil
}
```

In `cmd/agentsview/session.go`, register the command — change the block at lines
34-40 to add `newSessionUsageCommand()`:

```go
	cmd.AddCommand(newSessionGetCommand())
	cmd.AddCommand(newSessionUsageCommand())
	cmd.AddCommand(newSessionListCommand())
	cmd.AddCommand(newSessionMessagesCommand())
	cmd.AddCommand(newSessionToolCallsCommand())
	cmd.AddCommand(newSessionExportCommand())
	cmd.AddCommand(newSessionSyncCommand())
	cmd.AddCommand(newSessionWatchCommand())
```

- [ ] **Step 4: Run test to verify it passes**

Run:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/agentsview/ -run TestRenderSessionUsageHuman -v`
Expected: PASS.

- [ ] **Step 5: Build and smoke-test both commands**

Run:

```bash
go fmt ./... && go vet ./cmd/agentsview/
make build
./agentsview session --format json usage does-not-exist-123 ; echo "exit=$?"
```

Expected: build succeeds; the command prints `session not found: ...` to stderr
and `exit=2`. (A real session id from your archive should print JSON with
`cost_usd`/`has_cost`; `agentsview token-use <id>` should print the same JSON
plus the deprecation note on stderr.)

- [ ] **Step 6: Commit**

```bash
git add cmd/agentsview/session_usage.go cmd/agentsview/session.go cmd/agentsview/session_usage_test.go
git commit -m "feat(cli): add 'session usage' command with cost estimate"
```

______________________________________________________________________

### Task A5: Documentation

**Files:**

- Modify: `CHANGELOG.md` (top unreleased section)

- Modify: `README.md` (wherever `token-use` is documented; if not documented,
  add a short `session usage` note near the CLI/commands section)

- [ ] **Step 1: Update CHANGELOG**

Add under the unreleased/next-version section (match the file's existing style):

```markdown
- Add `agentsview session usage <id>`: per-session token statistics plus a cost
  estimate (`cost_usd` / `has_cost`), computed from the model-pricing catalog.
- Deprecate `agentsview token-use`; it remains as an alias of `session usage`
  and now also reports cost. Prefer `session usage`.
```

- [ ] **Step 2: Update README**

Search: `rg -n "token-use" README.md`. For each mention, document
`session usage <id>` as canonical and note `token-use` is a deprecated alias. If
there are no mentions, add a one-line entry for `session usage <id>` next to the
other `session` subcommands.

- [ ] **Step 3: Format and commit**

Run: `mdformat --wrap 80 CHANGELOG.md README.md` (if `mdformat` is installed;
otherwise skip — the pre-commit hook will format).

```bash
git add CHANGELOG.md README.md
git commit -m "docs: document session usage and token-use deprecation"
```

- [ ] **Step 4: Full agentsview test + lint gate**

Run: `make test && make vet` Expected: PASS. (Optionally `make lint` if
golangci-lint is set up locally.)

______________________________________________________________________

## Phase B — roborev (`/Users/wesm/code/roborev`)

> All Phase B steps run with working directory `/Users/wesm/code/roborev`.

### Task B1: agentsview version capability detection

Detect whether the installed agentsview supports `session usage` (>= 0.30.0)
while keeping the existing >= 0.15.0 floor.

**Files:**

- Modify: `internal/tokens/tokens.go`

- Test: `internal/tokens/tokens_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/tokens/tokens_test.go`:

```go
func TestParseVersion_Capabilities(t *testing.T) {
	cases := []struct {
		name                       string
		out                        string
		wantSupported, wantUsage   bool
		wantParsed                 bool
	}{
		{"too old", "agentsview v0.14.9", false, false, true},
		{"floor", "agentsview v0.15.0", true, false, true},
		{"between", "agentsview v0.29.0", true, false, true},
		{"usage", "agentsview v0.30.0", true, true, true},
		{"newer", "agentsview v1.2.3", true, true, true},
		{"garbage", "not a version", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sup, usage, parsed := parseVersion([]byte(c.out))
			if sup != c.wantSupported || usage != c.wantUsage ||
				parsed != c.wantParsed {
				t.Errorf("parseVersion(%q) = (%v,%v,%v), want (%v,%v,%v)",
					c.out, sup, usage, parsed,
					c.wantSupported, c.wantUsage, c.wantParsed)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tokens/ -run TestParseVersion_Capabilities -v`
Expected: FAIL — `parseVersion` returns 2 values, not 3.

- [ ] **Step 3: Implement version capability**

In `internal/tokens/tokens.go`:

(a) Add the new threshold and a comparison helper next to `minVersion`:

```go
// minVersion is the minimum agentsview version that supports any
// token data (the original token-use subcommand, 0.15.0).
var minVersion = [3]int{0, 15, 0}

// sessionUsageMinVersion is the minimum agentsview version that
// supports `session usage` (tokens + cost estimate).
var sessionUsageMinVersion = [3]int{0, 30, 0}

// geVersion reports whether ver >= min (lexicographic major.minor.patch).
func geVersion(ver, min [3]int) bool {
	for i := range 3 {
		if ver[i] != min[i] {
			return ver[i] > min[i]
		}
	}
	return true
}
```

(b) Replace `parseVersion` to return capabilities:

```go
// parseVersion extracts agentsview's version and reports
// capabilities: supported (>= minVersion, any token data),
// sessionUsage (>= sessionUsageMinVersion, cost estimate), and
// parsed (a version string was found at all).
func parseVersion(out []byte) (supported, sessionUsage, parsed bool) {
	m := versionRe.FindSubmatch(out)
	if m == nil {
		return false, false, false
	}
	var ver [3]int
	for i := range 3 {
		ver[i], _ = strconv.Atoi(string(m[i+1]))
	}
	return geVersion(ver, minVersion),
		geVersion(ver, sessionUsageMinVersion), true
}
```

(c) Add a cached capability field and update `resolveAgentsview` to return it.
Change the cache vars:

```go
var (
	versionMu          sync.Mutex
	versionProbe       versionState
	cachedBin          string
	cachedSessionUsage bool // valid when versionProbe == versionOK
)
```

Update `ResetVersionCache` to also clear it:

```go
func ResetVersionCache() {
	versionMu.Lock()
	defer versionMu.Unlock()
	versionProbe = versionUnchecked
	cachedBin = ""
	cachedSessionUsage = false
}
```

Change `resolveAgentsview`'s signature to
`func resolveAgentsview(ctx context.Context) (string, bool, bool)` and return
`cachedSessionUsage` alongside the existing results. The cache-hit branches
return `bin, true, cachedSessionUsage` (for `versionOK`) and `"", false, false`
(for `versionTooOld`). After probing:

```go
	supported, sessionUsage, parsed := parseVersion(out)

	versionMu.Lock()
	defer versionMu.Unlock()

	if cachedBin == bin {
		switch versionProbe {
		case versionOK:
			return bin, true, cachedSessionUsage
		case versionTooOld:
			return "", false, false
		}
	}

	if supported {
		versionProbe = versionOK
		cachedBin = bin
		cachedSessionUsage = sessionUsage
		return bin, true, sessionUsage
	}
	if parsed {
		versionProbe = versionTooOld
		cachedBin = bin
	}
	return "", false, false
```

(Leave the early `LookPath` / cache-hit block at the top of the function
returning the same three values: `bin, true, cachedSessionUsage` and
`"", false, false`.)

(d) Update the one existing caller in `FetchForSession` so the package compiles
now; B3 will use the new value. Change:

```go
	binPath, ok := resolveAgentsview(ctx)
```

to:

```go
	binPath, ok, _ := resolveAgentsview(ctx)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tokens/ -run TestParseVersion_Capabilities -v`
Expected: PASS (package compiles; the `_` discard keeps `FetchForSession` valid
until B3 consumes the capability).

- [ ] **Step 5: Commit**

```bash
git add internal/tokens/tokens.go internal/tokens/tokens_test.go
git commit -m "feat(tokens): detect agentsview session-usage capability by version"
```

______________________________________________________________________

### Task B2: Cost fields + FormatSummary

**Files:**

- Modify: `internal/tokens/tokens.go`

- Test: `internal/tokens/tokens_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/tokens/tokens_test.go`:

```go
func TestFormatSummary_WithCost(t *testing.T) {
	u := Usage{OutputTokens: 28800, PeakContextTokens: 118000,
		CostUSD: 0.42, HasCost: true}
	got := u.FormatSummary()
	want := "118.0k ctx · 28.8k out · ~$0.42"
	if got != want {
		t.Errorf("FormatSummary() = %q, want %q", got, want)
	}
}

func TestFormatSummary_NoCost(t *testing.T) {
	u := Usage{OutputTokens: 28800, PeakContextTokens: 118000}
	got := u.FormatSummary()
	want := "118.0k ctx · 28.8k out"
	if got != want {
		t.Errorf("FormatSummary() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tokens/ -run TestFormatSummary -v` Expected: FAIL —
`CostUSD`/`HasCost` not fields of `Usage`.

- [ ] **Step 3: Add cost fields and extend FormatSummary**

In `internal/tokens/tokens.go`, extend the structs:

```go
type Usage struct {
	OutputTokens      int64   `json:"total_output_tokens,omitempty"`
	PeakContextTokens int64   `json:"peak_context_tokens,omitempty"`
	CostUSD           float64 `json:"cost_usd,omitempty"`
	HasCost           bool    `json:"has_cost,omitempty"`
}

type agentsviewResponse struct {
	SessionID         string  `json:"session_id"`
	Agent             string  `json:"agent"`
	Project           string  `json:"project"`
	OutputTokens      int64   `json:"total_output_tokens"`
	PeakContextTokens int64   `json:"peak_context_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	HasCost           bool    `json:"has_cost"`
}
```

Extend `FormatSummary` to append cost when present:

```go
func (u Usage) FormatSummary() string {
	if u.PeakContextTokens == 0 && u.OutputTokens == 0 {
		return ""
	}
	s := fmt.Sprintf(
		"%s ctx · %s out",
		formatCount(u.PeakContextTokens),
		formatCount(u.OutputTokens),
	)
	if u.HasCost {
		s += fmt.Sprintf(" · ~$%.2f", u.CostUSD)
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tokens/ -run TestFormatSummary -v` Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tokens/tokens.go internal/tokens/tokens_test.go
git commit -m "feat(tokens): add cost fields and render ~\$cost in summary"
```

______________________________________________________________________

### Task B3: Command selection + exit-code handling in FetchForSession

**Files:**

- Modify: `internal/tokens/tokens.go`

- Test: `internal/tokens/tokens_test.go`

- [ ] **Step 1: Write the failing test (exit-code classification)**

The exit-code logic is extracted into a pure helper so it is testable without
exec. Add to `internal/tokens/tokens_test.go`:

```go
func TestClassifyExit(t *testing.T) {
	cases := []struct {
		code      int
		wantAvail bool // true => parse stdout; false => unavailable
		wantErr   bool
	}{
		{0, true, false},
		{2, false, false}, // not found -> unavailable, no error
		{3, false, false}, // no data  -> unavailable, no error
		{1, false, true},  // operational error
		{7, false, true},  // unexpected -> error
	}
	for _, c := range cases {
		avail, isErr := classifyUsageExit(c.code)
		if avail != c.wantAvail || isErr != c.wantErr {
			t.Errorf("classifyUsageExit(%d) = (%v,%v), want (%v,%v)",
				c.code, avail, isErr, c.wantAvail, c.wantErr)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tokens/ -run TestClassifyExit -v` Expected: FAIL —
`classifyUsageExit undefined`.

- [ ] **Step 3: Implement classifier + rewrite FetchForSession**

In `internal/tokens/tokens.go`, add the classifier:

```go
// classifyUsageExit maps an agentsview exit code to behavior:
// available (parse stdout) for 0; unavailable (nil, no error) for 2
// (not found) and 3 (no data); otherwise an operational error.
func classifyUsageExit(code int) (available, isErr bool) {
	switch code {
	case 0:
		return true, false
	case 2, 3:
		return false, false
	default:
		return false, true
	}
}
```

Rewrite `FetchForSession` to select the command by capability and use the
classifier:

```go
// FetchForSession queries agentsview for a session's token usage and
// cost. It calls `session usage` on agentsview >= 0.30.0 (tokens +
// cost) and falls back to `token-use` on 0.15.0–0.29.x (tokens only).
// Returns (nil, nil) when agentsview is missing, too old, or the
// session/usage is unavailable.
func FetchForSession(
	ctx context.Context, sessionID string,
) (*Usage, error) {
	if sessionID == "" {
		return nil, nil
	}

	binPath, ok, sessionUsage := resolveAgentsview(ctx)
	if !ok {
		return nil, nil
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var args []string
	if sessionUsage {
		args = []string{"session", "--format", "json", "usage", sessionID}
	} else {
		args = []string{"token-use", sessionID}
	}

	out, err := exec.CommandContext(cmdCtx, binPath, args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			available, isErr := classifyUsageExit(exitErr.ExitCode())
			if !available {
				if isErr {
					return nil, fmt.Errorf(
						"agentsview %s: exit %d: %s",
						args[0], exitErr.ExitCode(),
						string(exitErr.Stderr),
					)
				}
				return nil, nil // unavailable (exit 2/3)
			}
			// available but Output() still errored: fall through to
			// parse the captured stdout below.
		} else {
			return nil, fmt.Errorf("agentsview %s: %w", args[0], err)
		}
	}

	var resp agentsviewResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse agentsview output: %w", err)
	}

	if resp.OutputTokens == 0 && resp.PeakContextTokens == 0 && !resp.HasCost {
		return nil, nil
	}
	return &Usage{
		OutputTokens:      resp.OutputTokens,
		PeakContextTokens: resp.PeakContextTokens,
		CostUSD:           resp.CostUSD,
		HasCost:           resp.HasCost,
	}, nil
}
```

- [ ] **Step 4: Run the full tokens package tests + vet**

Run: `go test ./internal/tokens/... && go vet ./internal/tokens/...` Expected:
PASS (B1, B2, B3 tests all compile and pass; the `resolveAgentsview` caller now
matches the 3-value signature).

- [ ] **Step 5: Commit**

```bash
git add internal/tokens/tokens.go internal/tokens/tokens_test.go
git commit -m "feat(tokens): select session usage by version, fix exit-code handling"
```

______________________________________________________________________

### Task B4: Build and live end-to-end verification

**Files:** none (verification only).

- [ ] **Step 1: Install the new agentsview build**

In the agentsview worktree
(`/Users/wesm/.superset/worktrees/agentsview/feat/session-cost-estimate`):

```bash
make install   # builds with embedded frontend, installs to ~/.local/bin or GOPATH
agentsview version
```

Expected: a version string. Confirm `~/.local/bin` (or the install target) is on
`PATH` ahead of any older agentsview.

**Note on version gating:** roborev requires agentsview `>= 0.30.0` for
`session usage`. If `make install` reports a dev/older version (the version is
tag-driven via ldflags), confirm the installed binary reports `>= 0.30.0`;
otherwise temporarily build with the version injected, e.g.
`go build -ldflags "-X main.version=0.30.0-dev" -tags fts5 -o ~/.local/bin/agentsview ./cmd/agentsview`,
so roborev selects `session usage`. (Adjust the `-X` target if `make build` uses
a different version variable.)

- [ ] **Step 2: Sanity-check the command directly**

```bash
agentsview session list --format json | head        # find a real session id
agentsview session --format json usage <real-id>     # expect cost_usd/has_cost
```

Expected: JSON with `total_output_tokens`, `peak_context_tokens`, `cost_usd`,
`has_cost`. For a recent Claude session, `has_cost:true`.

- [ ] **Step 3: Build roborev and run a review**

In `/Users/wesm/code/roborev`:

```bash
go build ./... && go install ./cmd/roborev
```

Trigger a review that produces a session (per roborev's normal flow, e.g. a
branch review via the daemon). Then:

```bash
roborev show <job-id>
```

Expected: the token line now reads `Tokens: 118.0k ctx · 28.8k out · ~$0.42`
(numbers will vary) for a Claude session with priced models.

- [ ] **Step 4: Verify the TUI**

Launch the roborev TUI and open the same review. Expected: the verdict/status
line shows `[118.0k ctx · 28.8k out · ~$0.42]`.

- [ ] **Step 5: Verify graceful fallback (optional)**

Point `PATH` at an older agentsview (< 0.30.0) or temporarily rename the new
binary, run `tokens.ResetVersionCache()`-equivalent by restarting the daemon,
and confirm a review still shows tokens (no cost, no errors). Restore the new
binary afterward.

- [ ] **Step 6: Final commit (if any verification tweaks were needed)**

```bash
git add -A && git commit -m "test: verify session usage cost end-to-end"
```

(Skip if Step 1–5 required no file changes.)

______________________________________________________________________

## Cross-repo contract (reference)

`agentsview session --format json usage <id>` / `agentsview token-use <id>` emit
identical JSON:

```json
{
  "session_id": "claude:abc-123",
  "agent": "claude-code",
  "project": "roborev",
  "total_output_tokens": 28800,
  "peak_context_tokens": 118000,
  "has_token_data": true,
  "cost_usd": 0.42,
  "has_cost": true,
  "models": ["claude-opus-4-6"],
  "server_running": false
}
```

Exit codes: `0` (token data or cost present), `2` (not found), `3` (neither),
`1` (operational error). `cost_usd` is `0` whenever `has_cost` is false.

## Pre-push cleanup

Before pushing either repo, delete the internal scratch design/plan docs in a
final commit (project convention — specs/plans under `docs/superpowers/` are not
pushed):

```bash
git rm docs/superpowers/specs/2026-05-22-session-usage-cost-estimate-design.md \
       docs/superpowers/plans/2026-05-22-session-usage-cost-estimate.md
git commit -m "chore: remove internal design/plan docs before push"
```
