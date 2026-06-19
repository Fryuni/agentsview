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
- Providers embed a base facade with no-op defaults for optional behavior.
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
type Provider interface {
	Definition() AgentDef
	Capabilities() Capabilities

	Discover(context.Context, []string) ([]SourceRef, error)
	WatchPlan(context.Context, []string) (WatchPlan, error)
	SourcesForChangedPath(context.Context, string) ([]SourceRef, error)
	FindSource(context.Context, FindSourceRequest) (SourceRef, bool, error)
	Fingerprint(context.Context, SourceRef) (SourceFingerprint, error)

	Parse(context.Context, ParseRequest) (ParseOutcome, error)
	ParseIncremental(
		context.Context,
		IncrementalRequest,
	) (IncrementalOutcome, bool, error)
}
```

`ProviderBase` implements every optional method with safe default behavior. It
does not implement `Parse`, so a concrete provider cannot satisfy `Provider`
without a real parser entry point.

```go
type ProviderBase struct {
	Def  AgentDef
	Caps Capabilities
}
```

Every provider should include a compile-time assertion:

```go
var _ Provider = (*CodexProvider)(nil)
```

## Embedding Pattern

The intended implementation pattern is explicit embedding. Providers embed
`ProviderBase` for default optional behavior, then embed or compose source
helpers for common layouts. The concrete provider implements only the hooks
where it differs from the defaults.

```go
type CodexProvider struct {
	ProviderBase

	Sources SiblingMetadataSourceSet
}

func NewCodexProvider() *CodexProvider {
	return &CodexProvider{
		ProviderBase: ProviderBase{
			Def:  codexAgentDef(),
			Caps: codexCapabilities(),
		},
		Sources: SiblingMetadataSourceSet{
			Base: JSONLSourceSet{
				Extensions: []string{".jsonl"},
				Recursive: true,
			},
			MetadataFiles: []string{CodexSessionIndexFilename},
		},
	}
}

func (p *CodexProvider) Discover(
	ctx context.Context,
	roots []string,
) ([]SourceRef, error) {
	return p.Sources.Discover(ctx, p.Def.Type, roots)
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
		Results: []ParseResult{{Session: *sess, Messages: msgs}},
	}, nil
}
```

For a simple JSONL provider, the provider may embed the source helper directly:

```go
type QwenProvider struct {
	ProviderBase
	DirectoryJSONLSourceSet
}
```

The embedded helper can provide `Discover`, `WatchPlan`, `FindSource`, and
`Fingerprint` behavior when its method signatures match the provider hook shape.
When a provider needs extra context or adaptation, it keeps the helper as a
named field and delegates to it. Either way, the provider remains the only
object registered with the engine.

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

`FindSource` replaces the current `FindSourceFunc` fallback model. It must cover
file-backed and database-backed providers because `FindSourceFile`,
`SourceMtime`, token usage commands, session watch, and export flows all need
provider-specific source lookup today.

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

## Parse Requests And Outcomes

```go
type ParseRequest struct {
	Source      SourceRef
	Fingerprint SourceFingerprint
	Machine     string
	ForceParse  bool
}

type ParseOutcome struct {
	Results            []ParseResult
	ExcludedSessionIDs []string
	SourceErrors       []SourceError
	ForceReplace       bool
	SkipReason         SkipReason
}

type SourceError struct {
	SourceKey   string
	DisplayPath string
	SessionID   string
	Err         error
	Retryable   bool
}
```

Runtime behavior:

- Whole-source parse failures return `error`.
- Multi-session providers return `SourceErrors` for per-session failures so good
  sessions can still be ingested.
- `Retryable` decides whether a failure can be cached by mtime.
- `ForceReplace` is the generic signal for full parses that must rewrite
  existing ordinals.
- `SkipReason` replaces implicit "nil session means skip" behavior.
- Providers do not write to the DB.
- Providers do not mutate, delete, or repair source files.

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
capability and implement the hook. Typed full-parse fallback replaces
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

## Provider Toolkit

The facade should include helper types for common provider patterns. These
helpers live below the provider abstraction; the engine still talks only to
`Provider`.

Helpers should be designed for embedding first. A helper with no provider
specific state can expose methods that satisfy provider hooks directly when it
is embedded. Helpers that need provider metadata or extra adaptation should be
composed as named fields and called by thin provider methods.

### ProviderBase

Embedded default implementation for optional hooks:

- empty discovery;
- empty watch plan;
- no changed-path classification;
- no source lookup;
- basic direct file fingerprinting only when configured;
- no incremental parse.

`ProviderBase` carries metadata and capabilities but does not implement `Parse`.

### JSONLSourceSet

A reusable JSONL source lister/fingerprinter for the common pattern of session
transcripts stored as `.jsonl` files.

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
specific helpers.

### DirectoryJSONLSourceSet

Specialized JSONL helper for layouts where project or workspace names come from
directory structure, such as `<project>/<session>.jsonl` or nested
`projects/<encoded-project>/chats/<id>.jsonl`.

### SiblingMetadataSourceSet

Wraps another source set and folds sibling files into watch plans and effective
fingerprints. This covers patterns like transcript plus metadata/title/index
files.

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

1. Load providers from a provider registry.
1. Ask each provider to discover `SourceRef` values for configured roots.
1. Dedupe source refs by provider and key.
1. Ask each provider for `SourceFingerprint`.
1. Run generic skip/data-version checks using `FingerprintKey` and fingerprint
   fields.
1. Attempt incremental parsing when the provider declares and implements it.
1. Call provider `Parse` for full parses.
1. Apply existing normalization and DB write paths to `ParseResult` values.
1. Persist source metadata, skip cache, excluded IDs, usage events, and parse
   diagnostics using the existing storage model.

Changed-path live sync becomes:

1. The watcher reports a changed path.
1. Each provider with matching roots can classify it through
   `SourcesForChangedPath`.
1. The engine processes the returned `SourceRef` values generically.

Source lookup becomes:

1. The engine checks stored DB `file_path` first when appropriate.
1. The engine asks the owning provider to find a source for the raw session ID.
1. The provider returns a `SourceRef`, not just a string path.
1. The engine can ask the provider for a fingerprint/source mtime from that
   reference.

## Registry

`parser.Registry` remains the stable metadata surface during migration, but the
source of truth shifts to providers.

Target API:

```go
func Providers() []Provider
func ProviderByType(AgentType) (Provider, bool)
func AgentByType(AgentType) (AgentDef, bool)
func AgentByPrefix(string) (AgentDef, bool)
```

`AgentByType` and `AgentByPrefix` can continue to return `AgentDef` for config,
settings, display, and export code. `AgentDef` source callbacks become legacy
compatibility fields during migration and are removed or deprecated once every
consumer uses providers.

## Migration Plan

The implementation should migrate all providers, grouped by source pattern:

1. Add provider core types, capability enum generation, and `ProviderBase`.
1. Add provider registry and registry tests while preserving current
   `parser.Registry`.
1. Add JSONL source helpers and tests.
1. Add source helper families for sibling metadata, virtual paths, and SQLite
   fan-out.
1. Wrap each current provider in a facade adapter that calls existing parser
   functions.
1. Move provider-specific stat, mtime, source lookup, watch plan, and
   changed-path logic from `sync.Engine` into providers.
1. Replace `processFile` switch with generic provider dispatch.
1. Port `parse-diff` to provider discovery and provider parse results.
1. Port `FindSourceFile` and `SourceMtime` to provider source lookup and
   fingerprinting.
1. Remove or deprecate old `AgentDef` source callback fields after all callers
   stop using them.

Migration should keep existing parser unit tests. Provider-level tests become
the required integration surface for future providers.

## Testing

Required tests:

- Provider registry completeness: every `AgentType` has exactly one provider.
- Prefix uniqueness and metadata parity with current registry behavior.
- Capability enum generation, JSON representation, and zero-value behavior.
- Capability conformance: unsupported fields should not be emitted in parsed
  output.
- JSONL source helper discovery, sorting, filtering, project extraction, and
  fingerprint tests.
- Sibling metadata fingerprint tests.
- SQLite fan-out source key and per-session error tests.
- Provider harness tests for discovery, fingerprint, parse, source lookup, and
  optional incremental parsing.
- Migration parity tests comparing provider output to current parser/process
  output during the transition.
- Sync integration tests for incremental Claude/Codex, multi-session sources,
  parse-diff, source mtime, source lookup, skip cache, usage events, sidecars,
  virtual paths, and title/metadata refreshes.
- Adding-provider checklist test that fails until registry, capabilities,
  fixtures, source behavior, and docs are present.

## Error Handling

Providers return structured errors and outcomes. The engine makes generic
decisions from those structures:

- whole-source failure: returned `error`;
- per-session failure: `SourceErrors`;
- retryable failure: do not cache skip by unchanged mtime;
- non-retryable failure: eligible for skip-cache persistence;
- full parse fallback from incremental: typed outcome flag;
- skipped non-session source: explicit `SkipReason`;
- existing-row rewrite required: `ForceReplace`.

Parse-diff treats provider `SourceErrors` as reportable parse errors, preserving
today's behavior for shared database sources.

## Documentation Updates

Implementation should update developer-facing docs to describe how to add a
provider:

1. add an `AgentType`;
1. implement a provider embedding `ProviderBase`;
1. select source helpers or implement provider-specific source hooks;
1. implement `Parse`;
1. set capabilities;
1. add fixtures and provider harness tests;
1. update README/config docs for default directories and environment variables.

## Success Criteria

- All current providers are registered through the provider facade.
- `sync.Engine` no longer has a provider-by-provider parse dispatch switch.
- Source shape is not inspected by the engine.
- Capability reports serialize to readable JSON names.
- Existing parser and sync tests pass after migration.
- Parse-diff continues to use the same provider path as normal sync.
- Adding a provider requires implementing the provider contract and fails tests
  until capabilities, source behavior, fixtures, and docs are present.
