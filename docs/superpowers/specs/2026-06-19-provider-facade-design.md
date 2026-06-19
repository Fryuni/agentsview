# Provider Facade Design

## Purpose

agentsview supports many agent formats, but parser integration is currently
spread across `parser.AgentDef`, parser-specific discovery functions, parser
function signatures, and a large `sync.Engine` switch. Adding a provider often
means touching several unrelated areas and relying on convention for optional
features such as tool calls, usage events, termination status, source mtime, and
incremental parsing.

This design adds a shared provider facade so adding or migrating a provider
means implementing one contract. The facade keeps provider source shape
internal, while the sync engine consumes normalized source identities,
fingerprints, and `ParseResult` values.

## Goals

- Migrate every existing provider to the facade, not only future providers.
- Keep `ParsedSession`, `ParsedMessage`, `ParsedToolCall`, `ParsedToolResult`,
  `ParsedUsageEvent`, and `ParseResult` as the normalized output contract.
- Remove the provider-by-provider `sync.Engine.processFile` dispatch switch.
- Make source discovery, source lookup, watch planning, fingerprinting, parsing,
  and optional incremental parsing provider-owned.
- Provide reusable provider helpers for common source layouts, especially JSONL
  file discovery.
- Make optional parsed features auditable through a concrete `Capabilities`
  struct.
- Preserve current SQLite schema, parse-diff semantics, skip-cache behavior, and
  parser output parity.

## Non-Goals

- Rewrite individual provider parsers from scratch.
- Change the persistent database schema as part of this refactor.
- Move DB writes into providers.
- Make source storage shape a global engine concern.
- Turn all providers into JSONL providers. JSONL helpers are shared utilities,
  not the abstraction boundary.

## Design Constraints

The provider facade must respect these constraints:

- Source shape belongs to the provider. The engine must not know whether a
  source is a JSONL file, SQLite row, sidecar, trace folder, import archive, or
  multiple files.
- Providers embed a base facade with zero-value no-op implementations for
  optional source behavior.
- Providers must implement `Parse`; the base facade must not provide a fake
  parse implementation.
- Capabilities use a concrete struct. The zero value of every capability field
  is unsupported.
- Capability enum string and JSON methods should be generated with
  `dmarkham/enumer`, because it supports generated `String`, JSON, and text
  marshal methods from one enum definition.
- All existing providers migrate to the new layer before the old sync dispatch
  is considered removed.

## Core Types

The provider contract should live near the parser boundary, for example in
`internal/parser/provider.go`, because it works with parser-owned normalized
types and agent metadata.

```go
type ProviderFactory interface {
	Definition() AgentDef
	Capabilities() Capabilities
	NewProvider(ProviderConfig) Provider
}

type ProviderConfig struct {
	Roots   []string
	Machine string
}

type Provider interface {
	Definition() AgentDef
	Capabilities() Capabilities

	Discover(context.Context) ([]SourceRef, error)
	WatchPlan(context.Context) (WatchPlan, error)
	SourcesForChangedPath(context.Context, ChangedPathRequest) ([]SourceRef, error)
	FindSource(context.Context, FindSourceRequest) (SourceRef, bool, error)
	Fingerprint(context.Context, SourceRef) (SourceFingerprint, error)

	Parse(context.Context, ParseRequest) (ParseOutcome, error)
	ParseIncremental(
		context.Context,
		IncrementalRequest,
	) (IncrementalOutcome, bool, error)
}
```

`ProviderFactory` is the registry surface. `Provider` is a config-bound instance
created by `NewProvider` for one engine, with that engine's configured roots and
machine. This keeps changed-path classification root-aware without requiring
mutable singleton providers or passing raw roots through every engine call.

`ProviderBase` implements every optional source method with safe zero-value
no-op behavior. It does not implement `Parse`, so a concrete provider cannot
satisfy `Provider` without a real parser entry point.

```go
type ProviderBase struct {
	Def    AgentDef
	Caps   Capabilities
	Config ProviderConfig
}
```

`ProviderBase` provides `Definition`, `Capabilities`, empty discovery, empty
watch plans, no changed-path classification, no source lookup, unsupported
fingerprints, and `(IncrementalOutcome{}, false, nil)` for incremental parsing.
That keeps the engine call surface uniform: every provider can be called through
the full `Provider` interface without feature-specific nil checks.

Reusable source helpers must not be generic indirection interfaces or provider
base classes. They are plain source-set structs such as `JSONLSourceSet`,
`DirectoryJSONLSourceSet`, or `SQLiteFanoutSourceSet`. A provider keeps a helper
as a named field and forwards the source methods it supports. That forwarding is
deliberate: it makes the provider's optional behavior visible at the concrete
type without adding another abstraction layer.

Every provider should include a compile-time assertion:

```go
var _ Provider = (*CodexProvider)(nil)
```

Embedding and delegation examples must be compile-tested as part of the provider
harness, so the documented pattern cannot drift into impossible Go.

## Embedding Pattern

The intended implementation pattern is: embed `ProviderBase`, keep source
helpers as named fields, and implement explicit forwarding methods for the
source behaviors the provider supports. `ProviderBase` keeps every optional
method callable; provider methods override only the useful defaults.

```go
type CodexProvider struct {
	ProviderBase
	sources SiblingMetadataSourceSet
}

func NewCodexProvider(cfg ProviderConfig) *CodexProvider {
	return &CodexProvider{
		ProviderBase: ProviderBase{
			Def:    codexAgentDef(),
			Caps:   codexCapabilities(),
			Config: cfg,
		},
		sources: SiblingMetadataSourceSet{
			Base: JSONLSourceSet{
				Agent:      AgentCodex,
				Roots:      cfg.Roots,
				Extensions: []string{".jsonl"},
				Recursive:  true,
			},
			MetadataFiles: []string{CodexSessionIndexFilename},
		},
	}
}

func (p *CodexProvider) Discover(
	ctx context.Context,
) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *CodexProvider) WatchPlan(
	ctx context.Context,
) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *CodexProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *CodexProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	return p.sources.FindSource(ctx, req)
}

func (p *CodexProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *CodexProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	sess, msgs, err := ParseCodexSession(
		req.Source.DisplayPath,
		req.Machine,
		false,
	)
	if err != nil || sess == nil {
		return ParseOutcome{}, err
	}
	return ParseOutcome{
		Results: []ParseResultOutcome{{
			Result:      ParseResult{Session: *sess, Messages: msgs},
			DataVersion: DataVersionCurrent,
		}},
	}, nil
}
```

For a simple JSONL provider, the same pattern uses a different source-set field:

```go
type QwenProvider struct {
	ProviderBase
	sources DirectoryJSONLSourceSet
}

func NewQwenProvider(cfg ProviderConfig) *QwenProvider {
	return &QwenProvider{
		ProviderBase: ProviderBase{
			Def:    qwenAgentDef(),
			Caps:   qwenCapabilities(),
			Config: cfg,
		},
		sources: DirectoryJSONLSourceSet{
			JSONLSourceSet: JSONLSourceSet{
				Agent:      AgentQwen,
				Roots:      cfg.Roots,
				Extensions: []string{".jsonl"},
				Recursive:  true,
			},
			ProjectFromPath: qwenProjectFromPath,
		},
	}
}
```

Providers that need one-off source behavior still embed `ProviderBase` and write
only the concrete methods they need:

```go
type VisualStudioCopilotProvider struct {
	ProviderBase
	traces VisualStudioTraceSourceSet
}

func (p *VisualStudioCopilotProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.traces.StrictFingerprint(ctx, source)
}
```

The rule is: embed `ProviderBase` once, compose source helpers as named fields,
and forward intentionally. Do not embed source helpers beside `ProviderBase`
when they define the same optional methods as the base; same-depth promoted
selectors will not give the intended override. Source behavior stays explicit:
there is no generic source-behavior interface and no runtime method table hidden
in the base.

## Source References

`SourceRef` is the engine-visible handle for provider-owned source data.

```go
type SourceRef struct {
	Provider       AgentType
	Key            string
	DisplayPath    string
	FingerprintKey string
	ProjectHint    string

	// Provider-owned payload. The sync engine passes this back to the
	// same provider and must not inspect it.
	Opaque any
}
```

Rules:

- `Key` is stable within the provider and suitable for logs and dedupe.
- `DisplayPath` is human-readable and may be a virtual path.
- `FingerprintKey` is the DB lookup key used for skip/data-version checks.
- `ProjectHint` is advisory and can be empty.
- `Opaque` is internal provider state. The engine treats it as an opaque token.

Backwards compatibility:

- Migrated providers should keep `FingerprintKey` compatible with the source key
  or stored `file_path` values already written by the legacy sync path whenever
  practical. Existing fingerprint and data-version metadata should continue to
  short-circuit unchanged sources after the facade migration.
- If a provider must change its lookup key or fingerprint identity, that
  provider migration must explicitly document the expected full resync or
  metadata transition. The facade migration itself must not silently force all
  providers through a full resync.
- Diagnostics, parse errors, and logs should surface stable fields such as
  `Provider`, `Key`, `DisplayPath`, and `FingerprintKey`. `Opaque` is never
  persisted or logged because it may contain provider-internal implementation
  details that are not stable across releases.

Watch and changed-path classification use provider-owned root metadata:

```go
type WatchPlan struct {
	Roots []WatchRoot
}

type WatchRoot struct {
	Path         string
	Recursive    bool
	IncludeGlobs []string
	ExcludeGlobs []string
	DebounceKey  string
}

type ChangedPathRequest struct {
	Path      string
	EventKind string
	WatchRoot string
}
```

`WatchRoot.Path` is the actual filesystem root the engine should watch.
`Recursive` controls watcher depth. Include and exclude globs are advisory
provider filters that allow broad OS watch roots without parsing every changed
file. `DebounceKey` groups related paths such as sibling metadata files and a
transcript. `ChangedPathRequest.WatchRoot` is the matched watch root, so the
provider can classify changes relative to the configured root that produced
them.

The provider owns the final changed-path decision. The engine may use
`IncludeGlobs` and `ExcludeGlobs` as coarse prefilters because the provider
supplied them, but `SourcesForChangedPath` must still tolerate unfiltered events
and apply authoritative provider-specific classification. Diagnostics should
report whether the provider accepted, ignored, or rejected a changed path.

`FindSource` replaces the current `FindSourceFunc` fallback model. It must cover
file-backed and database-backed providers because `FindSourceFile`,
`SourceMtime`, token usage commands, session watch, and export flows all need
provider-specific source lookup today.

```go
type FindSourceRequest struct {
	RawSessionID       string
	FullSessionID      string
	StoredFilePath     string
	FingerprintKey     string
	RequireFreshSource bool
}
```

Stored DB `file_path` values are advisory compatibility keys. The engine passes
them through `FindSourceRequest`, but the provider decides whether they are real
paths, virtual paths, row keys, or obsolete source hints. The engine must not
`stat`, split, glob, or otherwise interpret a stored path before asking the
provider to resolve it.

`RequireFreshSource` means the caller needs a source reference the provider has
verified against current source state. The provider may use stored metadata as a
hint, but it must confirm the current file, database row, import record, or
virtual source can still be read or fingerprinted. Filesystem providers usually
check the current path; database and virtual providers can satisfy this by
resolving the current logical record. If the source no longer exists, the
provider returns `(SourceRef{}, false, nil)`. If the source might exist but
cannot be checked because of an I/O or database failure, it returns an error.
When `RequireFreshSource` is false, the provider may return the best
compatibility match for display/export flows that can tolerate stale source
hints.

## Fingerprints

The provider owns fingerprint calculation because source freshness can depend on
composite state:

- transcript files plus sibling metadata;
- SQLite database file mtimes;
- virtual paths for one logical session inside a database;
- sidecar files that supersede encrypted or summary sources;
- trace folders containing related files.

```go
type SourceFingerprint struct {
	Key     string
	Size    int64
	MTimeNS int64
	Inode   uint64
	Device  uint64
	Hash    string
}
```

The engine uses fingerprints for generic skip/data-version checks and stores the
same normalized source file metadata it stores today. Hashes remain optional
where they are expensive or not meaningful.

Fingerprinting must stay cheap enough for the sync hot path. Acceptance tests or
benchmarks should use representative large roots and composite sources with
concrete pass criteria:

- no full-root content hashing during unchanged sync;
- no recursive directory walk for every source when discovery already produced
  the source list;
- file-backed fingerprints are bounded by the source plus declared sibling
  metadata files;
- database fan-out fingerprints reuse database-level metadata plus row/session
  identifiers instead of scanning unrelated rows;
- any provider that requires a full content hash documents why mtime, size,
  inode/device, row metadata, or sidecar metadata are insufficient and includes
  a benchmark budget for that provider.

## Parse Requests And Outcomes

```go
type ParseRequest struct {
	Source      SourceRef
	Fingerprint SourceFingerprint
	Machine     string
	ForceParse  bool
}

type ParseOutcome struct {
	Results            []ParseResultOutcome
	ExcludedSessionIDs []string
	SourceErrors       []SourceError
	ForceReplace       bool
	SkipReason         SkipReason
}

type ParseResultOutcome struct {
	Result      ParseResult
	DataVersion DataVersionState
	RetryReason string
}

type SourceError struct {
	SourceKey   string
	DisplayPath string
	SessionID   string
	Err         error
	Retryable   bool
}

type DataVersionState uint8

const (
	DataVersionUnspecified DataVersionState = iota
	DataVersionCurrent
	DataVersionNeedsRetry
)

type SkipReason uint8

const (
	SkipNone SkipReason = iota
	SkipNoSession
	SkipUnsupportedSource
	SkipNonInteractive
	SkipShadowedBySidecar
)
```

Runtime behavior:

- Whole-source parse failures return `error`.
- Multi-session providers return one `ParseResultOutcome` per successfully
  parsed session and `SourceErrors` for per-session failures, so good sessions
  can still be ingested.
- `Retryable` decides whether a failure can be cached by mtime.
- `ForceReplace` is the generic signal for full parses that must rewrite
  existing ordinals.
- `DataVersionCurrent` means the corresponding successful result represents the
  current parser data version for that session.
- `DataVersionNeedsRetry` means successful fallback results may be written, but
  the session remains eligible for a future parse at the current data version.
  The engine must not persist a current data-version marker or clean skip-cache
  entry for that session. `RetryReason` records why, for example an
  Antigravity-style lower-resolution fallback.
- `DataVersionUnspecified` is allowed only during migration adapters; provider
  harness tests should require new providers to set either `DataVersionCurrent`
  or `DataVersionNeedsRetry` for every returned result.
- Mixed data-version states are valid for multi-session sources. One result can
  be current while another result from the same source needs retry, and a
  retryable `SourceError` affects only the failed session unless the provider
  reports a whole-source `error`.
- `SkipReason` replaces implicit "nil session means skip" behavior. Skips are
  explicit outcomes and should not be conflated with retryable parse failures.
- Providers do not write to the DB.
- Providers do not mutate, delete, or repair source files.

Skip reason semantics:

- `SkipNone`: the provider did not skip the source.
- `SkipNoSession`: the source is valid for the provider but does not contain a
  session after parsing.
- `SkipUnsupportedSource`: the source was discovered by broad matching but is
  not a supported source shape for this provider.
- `SkipNonInteractive`: the source is intentionally excluded because it is not a
  user-facing session.
- `SkipShadowedBySidecar`: a sibling or replacement source supersedes this
  source. The replacement write path should use `ForceReplace` when existing
  rows need to be rewritten.

Unchanged-source skips remain engine skip-cache decisions based on provider
fingerprints. They are not returned as `ParseOutcome` values.

## Incremental Parsing

Incremental parsing is optional provider behavior.

```go
type IncrementalRequest struct {
	Source       SourceRef
	Fingerprint  SourceFingerprint
	SessionID    string
	Offset       int64
	StartOrdinal int
	Machine      string
}

type IncrementalOutcome struct {
	SessionID            string
	Messages             []ParsedMessage
	EndedAt              time.Time
	ConsumedBytes        int64
	MessageCount         int
	UserMessageCount     int
	TotalOutputTokens    int
	PeakContextTokens    int
	HasTotalOutputTokens bool
	HasPeakContextTokens bool
	ForceFullParse       bool
	ForceReplace         bool
}
```

`ProviderBase.ParseIncremental` returns `(IncrementalOutcome{}, false, nil)`.
Providers that support append-only incremental parsing set the relevant source
capability and implement the method. Typed full-parse fallback replaces
provider-specific error checks in the engine.

## Capabilities

Capabilities use a concrete struct and an iota enum. The zero value maps to
unsupported.

```go
//go:generate go run github.com/dmarkham/enumer -type=CapabilitySupport -json -text -transform=snake -trimprefix=Capability -output=capabilitysupport_enumer.go

type CapabilitySupport uint8

const (
	CapabilityUnsupported CapabilitySupport = iota
	CapabilitySupported
	CapabilityNotApplicable
)
```

`enumer` is preferred over plain `stringer` here because it can generate
`String`, JSON marshal/unmarshal, text marshal/unmarshal, value listing, and
validation helpers from the same enum definition.

The struct should group source mechanics and parsed-content features:

```go
type Capabilities struct {
	Source  SourceCapabilities
	Content ContentCapabilities
}

type SourceCapabilities struct {
	DiscoverSources       CapabilitySupport
	WatchSources          CapabilitySupport
	ClassifyChangedPath   CapabilitySupport
	FindSource            CapabilitySupport
	CompositeFingerprint  CapabilitySupport
	IncrementalAppend     CapabilitySupport
	MultiSessionSource    CapabilitySupport
	PerSessionErrors      CapabilitySupport
	ExcludedSessions      CapabilitySupport
	ForceReplaceOnParse   CapabilitySupport
}

type ContentCapabilities struct {
	FirstMessage         CapabilitySupport
	SessionName          CapabilitySupport
	Cwd                  CapabilitySupport
	GitBranch            CapabilitySupport
	Relationships        CapabilitySupport
	Subagents            CapabilitySupport
	Thinking             CapabilitySupport
	ToolCalls            CapabilitySupport
	ToolResults          CapabilitySupport
	ToolResultEvents     CapabilitySupport
	PerMessageTokenUsage CapabilitySupport
	AggregateUsageEvents CapabilitySupport
	TerminationStatus    CapabilitySupport
	MalformedLineCount   CapabilitySupport
	TruncationStatus     CapabilitySupport
	Model                CapabilitySupport
	StopReason           CapabilitySupport
}
```

Providers set supported or not-applicable values explicitly. Missing fields stay
unsupported. Capability tests ensure a provider does not emit normalized fields
that contradict unsupported declarations.

Capability semantics are intentionally strict:

- `CapabilityUnsupported` means this provider does not currently emit or
  implement the feature. Tests should fail if normalized output contains that
  feature.
- `CapabilitySupported` means the feature is implemented and covered by provider
  fixtures or source-behavior tests.
- `CapabilityNotApplicable` means the upstream source format cannot represent
  the feature. It is not a placeholder for unfinished implementation work.

Generated enum tooling should be reproducible:

- keep a `tools.go` file with a `tools` build tag and a blank import for
  `github.com/dmarkham/enumer`;
- pin the enumer module in `go.mod` and commit `go.sum`;
- commit the generated `capabilitysupport_enumer.go` file;
- include a generator check, for example
  `go generate ./internal/parser && git diff --exit-code -- internal/parser/capabilitysupport_enumer.go`.

## Provider Toolkit

The facade should include helper types for common provider patterns. These
helpers live below the provider abstraction; the engine still talks only to
`Provider`.

Helpers should be plain source-set utilities. A helper stores the source-layout
state for one pattern and exposes methods with the same signatures as provider
source methods. Providers use helpers through named fields and choose which
methods to forward. Unforwarded methods fall back to `ProviderBase` no-ops.

### ProviderBase

Embedded default implementation for optional provider methods:

- empty discovery;
- empty watch plan;
- no changed-path classification;
- no source lookup;
- unsupported fingerprinting;
- no incremental parse.

`ProviderBase` carries metadata and capabilities but does not implement `Parse`.

### JSONLSourceSet

A reusable JSONL source lister/fingerprinter for the common pattern of session
transcripts stored as `.jsonl` files. It does not implement `Provider` by
itself; concrete providers hold it as a field and forward discovery, watch
planning, changed-path classification, source lookup, and fingerprinting when
those behaviors apply.

Expected options:

- root directories;
- recursive or shallow traversal;
- extension set, defaulting to `.jsonl`;
- path filters;
- project extraction from path;
- source key derivation from path;
- stable sorting;
- optional symlink directory handling to match current discovery behavior.

This helper should cover simple JSONL providers and serve as the base for more
specific source sets.

### DirectoryJSONLSourceSet

Specialized JSONL helper for layouts where project or workspace names come from
directory structure, such as `<project>/<session>.jsonl` or nested
`projects/<encoded-project>/chats/<id>.jsonl`.

### SiblingMetadataSourceSet

Wraps another source-set implementation and folds sibling files into watch plans
and effective fingerprints. This covers patterns like transcript plus
metadata/title/index files.

### SQLiteFanoutSourceSet

Creates one or many `SourceRef` values from a shared SQLite source while keeping
table and row details provider-owned. It supports providers where one database
file represents many logical sessions.

### VirtualPath Helpers

Providers that expose one logical session inside a shared source can continue to
return virtual display paths, but the virtual path format should be provider
owned and resolved through provider methods rather than hard-coded in sync.

## Sync Engine Flow

The generic engine flow becomes:

1. Load provider factories from the provider registry.
1. Create config-bound providers with
   `ProviderConfig{Roots: roots, Machine: machine}`.
1. Ask each provider to discover `SourceRef` values for configured roots.
1. Dedupe source refs by provider and key.
1. Ask each provider for `SourceFingerprint`.
1. Run generic skip/data-version checks using `FingerprintKey` and fingerprint
   fields.
1. Attempt incremental parsing when the provider declares and implements it.
1. Call provider `Parse` for full parses.
1. Apply existing normalization and DB write paths to each
   `ParseResultOutcome.Result` value.
1. Persist source metadata, skip cache, excluded IDs, usage events, and parse
   diagnostics using the existing storage model.

Changed-path live sync becomes:

1. The watcher reports a changed path.
1. The engine finds providers whose `WatchPlan` roots match the changed path.
1. Each matched provider classifies it through `SourcesForChangedPath` with a
   `ChangedPathRequest` that includes the changed path, event kind, and matched
   watch root.
1. The engine processes the returned `SourceRef` values generically.

Source lookup becomes:

1. The engine loads the owning provider and stored session metadata.
1. The engine passes the raw session ID, full session ID, stored `file_path`,
   and stored fingerprint key to `FindSourceRequest`.
1. The provider returns a `SourceRef`, not just a string path.
1. The engine can ask the provider for a fingerprint/source mtime from that
   reference.

The engine must treat stored `file_path` as an advisory compatibility key. It
must not check that path first or assume it is a filesystem path, because some
providers expose virtual paths or logical sessions inside a shared source.

## Registry

`parser.Registry` remains the stable metadata surface during migration, but the
source of truth shifts to providers.

Target API:

```go
func ProviderFactories() []ProviderFactory
func ProviderFactoryByType(AgentType) (ProviderFactory, bool)
func NewProvider(AgentType, ProviderConfig) (Provider, bool)
func AgentByType(AgentType) (AgentDef, bool)
func AgentByPrefix(string) (AgentDef, bool)
```

`AgentByType` and `AgentByPrefix` can continue to return `AgentDef` for config,
settings, display, and export code. `AgentDef` source callbacks become legacy
compatibility fields during migration and are removed or deprecated once every
consumer uses providers.

## Migration Plan

The implementation should migrate all providers, grouped by source pattern:

1. Add provider core types, `ProviderBase` defaults, contract invariants, and
   compile-tested embedding examples.
1. Add capability enum generation, pinned `enumer` tooling, and generated-file
   verification.
1. Add provider factory registry tests while preserving current
   `parser.Registry`.
1. Add JSONL source-set helpers and tests for simple file-backed JSONL
   providers.
1. Migrate simple JSONL providers with acceptance tests for discovery,
   fingerprint, parse output, skip-cache metadata, and data-version behavior.
1. Add and migrate sibling/composite source providers with acceptance tests for
   watch planning, composite fingerprints, sidecar/title refreshes, and changed
   path classification.
1. Add and migrate virtual-path and SQLite fan-out providers with acceptance
   tests for stored advisory paths, logical session lookup, per-session errors,
   and source mtime behavior.
1. Add and migrate non-file import/database providers with acceptance tests for
   `FindSource`, fingerprinting, and unsupported source mechanics.
1. Move source-processing callers onto providers: full sync, changed-path sync,
   and `SyncSingleSession`.
1. Move lookup/watch callers onto providers: session watch flows, export/source
   lookup, source mtime, and token-usage raw source probing.
1. Move diagnostic and comparison callers onto providers: parse-diff and parse
   diagnostics.
1. Run a transitional parity phase where old `processFile` and new provider
   dispatch can be compared in tests. The parity assertions must include parsed
   output, excluded IDs, skip-cache decisions, data-version writes, retry-needed
   outcomes, persisted source metadata, and diagnostics.
1. Replace `processFile` switch with generic provider dispatch only after the
   grouped parity tests pass.
1. Remove or deprecate old `AgentDef` source callback fields after all callers
   stop using them.

Migration should keep existing parser unit tests. Provider-level tests become
the required integration surface for future providers.

## Testing

Required tests:

- Provider registry completeness: every `AgentType` has exactly one provider.
- Prefix uniqueness and metadata parity with current registry behavior.
- Provider factory instantiation: configured roots and machine are copied into
  config-bound providers and do not mutate singleton registry state.
- `ProviderBase` contract tests: zero-value optional methods are callable and
  return the documented no-op results.
- Compile-tested embedding examples for `ProviderBase`, named JSONL source-set
  fields, sibling metadata source-set fields, and explicit concrete method
  overrides.
- Capability enum generation, JSON representation, and zero-value behavior.
- Capability conformance: unsupported fields should not be emitted in parsed
  output.
- Capability semantics: `not_applicable` is accepted only for fields impossible
  in the upstream source format, not for unimplemented work.
- JSONL source helper discovery, sorting, filtering, project extraction, and
  fingerprint tests.
- Sibling metadata fingerprint tests.
- Watch-plan and changed-path classification tests for recursive roots,
  non-recursive roots, include/exclude filters, and sibling metadata debounce
  groups.
- Stored advisory path tests proving `FindSourceRequest.StoredFilePath` is
  interpreted only by the provider.
- SQLite fan-out source key, virtual path, and per-session error tests.
- Data-version tests for current, skipped, retry-needed, and mixed per-session
  parse outcomes from one source.
- Fingerprint performance tests or benchmarks for large roots and composite
  sources, with the pass criteria from the fingerprint section.
- Provider harness tests for discovery, fingerprint, parse, source lookup, and
  optional incremental parsing.
- Parse diagnostic tests proving stable source fields are reported and opaque
  payloads are not serialized or logged.
- Migration parity tests comparing provider output to current parser/process
  output during the transition, including skip-cache, data-version writes,
  persisted source metadata, diagnostics, excluded IDs, and retry-needed
  behavior.
- Sync integration tests for incremental Claude/Codex, multi-session sources,
  parse-diff, source mtime, source lookup, skip cache, usage events, sidecars,
  virtual paths, and title/metadata refreshes.
- Caller migration tests for full sync, changed-path sync, `SyncSingleSession`,
  session watch flows, export/source lookup, token usage raw-source probing,
  source mtime, and parse diagnostics.
- Generated tooling check for `enumer` output.
- Adding-provider checklist test that fails until registry, capabilities,
  fixtures, source behavior, and docs are present.

## Error Handling

Providers return structured errors and outcomes. The engine makes generic
decisions from those structures:

- whole-source failure: returned `error`;
- per-session failure: `SourceErrors`, while successful sessions from the same
  source are still written;
- retryable failure: do not cache skip by unchanged mtime and do not mark the
  affected source/session current for the parser data version;
- non-retryable failure: eligible for skip-cache persistence;
- full parse fallback from incremental: typed outcome flag;
- successful lower-resolution fallback: per-result `DataVersionNeedsRetry` plus
  `RetryReason`;
- skipped non-session source: explicit `SkipReason`;
- existing-row rewrite required: `ForceReplace`.

Parse-diff treats provider `SourceErrors` as reportable parse errors, preserving
today's behavior for shared database sources.

## Documentation Updates

Implementation should update developer-facing docs to describe how to add a
provider:

1. add an `AgentType`;
1. implement a provider embedding `ProviderBase`;
1. select source helpers or implement provider-specific source methods;
1. implement `Parse`;
1. set capabilities;
1. add fixtures and provider harness tests;
1. update README/config docs for default directories and environment variables.

## Success Criteria

- All current providers are registered through the provider facade.
- `sync.Engine` no longer has a provider-by-provider parse dispatch switch.
- Source shape is not inspected by the engine.
- Capability reports serialize to readable JSON names.
- Capability enum generation is pinned and reproducible.
- Configured roots are provider-instance state, not mutable singleton state.
- `ProviderBase` zero-value optional methods are callable by the engine.
- Stored `file_path` values are provider-owned advisory lookup keys.
- Retry-needed outcomes preserve future parse eligibility instead of marking
  data versions current.
- Existing parser and sync tests pass after migration.
- Parse-diff continues to use the same provider path as normal sync.
- Adding a provider requires implementing the provider contract and fails tests
  until capabilities, source behavior, fixtures, and docs are present.
