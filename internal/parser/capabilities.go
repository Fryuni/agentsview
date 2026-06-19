package parser

//go:generate go run github.com/dmarkham/enumer -type=CapabilitySupport -json -text -transform=snake -trimprefix=Capability -output=capabilitysupport_enumer.go

// CapabilitySupport describes whether a provider implements or can represent a
// source or content feature. The zero value is unsupported.
type CapabilitySupport uint8

const (
	CapabilityUnsupported CapabilitySupport = iota
	CapabilitySupported
	CapabilityNotApplicable
)

// Capabilities groups provider source mechanics and parsed-content features.
type Capabilities struct {
	Source  SourceCapabilities
	Content ContentCapabilities
}

// SourceCapabilities declares optional source mechanics implemented by a
// provider.
type SourceCapabilities struct {
	DiscoverSources      CapabilitySupport
	WatchSources         CapabilitySupport
	ClassifyChangedPath  CapabilitySupport
	FindSource           CapabilitySupport
	CompositeFingerprint CapabilitySupport
	IncrementalAppend    CapabilitySupport
	MultiSessionSource   CapabilitySupport
	PerSessionErrors     CapabilitySupport
	ExcludedSessions     CapabilitySupport
	ForceReplaceOnParse  CapabilitySupport
}

// ContentCapabilities declares optional normalized content fields a provider
// may emit.
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
