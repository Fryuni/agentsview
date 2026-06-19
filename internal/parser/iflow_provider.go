package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*iflowProvider)(nil)

type iflowProviderFactory struct {
	def AgentDef
}

func newIflowProviderFactory(def AgentDef) ProviderFactory {
	return iflowProviderFactory{def: cloneAgentDef(def)}
}

func (f iflowProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f iflowProviderFactory) Capabilities() Capabilities {
	return iflowProviderCapabilities()
}

func (f iflowProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &iflowProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   iflowProviderCapabilities(),
			Config: cfg,
		},
		sources: newIflowSourceSet(cfg.Roots),
	}
}

type iflowProvider struct {
	ProviderBase
	sources DirectoryJSONLSourceSet
}

func (p *iflowProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *iflowProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *iflowProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *iflowProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	req.RawSessionID = extractIflowBaseSessionID(req.RawSessionID)
	return p.sources.FindSource(ctx, req)
}

func (p *iflowProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *iflowProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("iflow source path unavailable")
	}
	project := firstNonEmptyJSONLString(
		req.Source.ProjectHint,
		directoryJSONLProjectFromPath(path),
	)
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	results, err := ParseIflowSession(path, project, machine)
	if err != nil {
		return ParseOutcome{}, err
	}
	if len(results) == 0 {
		return ParseOutcome{
			ResultSetComplete: true,
			SkipReason:        SkipNoSession,
		}, nil
	}
	out := make([]ParseResultOutcome, 0, len(results))
	for _, result := range results {
		if req.Fingerprint.Hash != "" {
			result.Session.File.Hash = req.Fingerprint.Hash
		}
		out = append(out, ParseResultOutcome{
			Result:      result,
			DataVersion: DataVersionCurrent,
		})
	}
	return ParseOutcome{
		Results:           out,
		ResultSetComplete: true,
	}, nil
}

func newIflowSourceSet(roots []string) DirectoryJSONLSourceSet {
	return NewDirectoryJSONLSourceSet(
		AgentIflow,
		roots,
		JSONLSourceSetOptions{
			IncludePath:       isIflowSourcePath,
			SessionIDFromPath: iflowSessionIDFromPath,
		},
	)
}

func isIflowSourcePath(root, path string) bool {
	name := filepath.Base(path)
	return strings.HasPrefix(name, "session-") &&
		strings.HasSuffix(name, ".jsonl")
}

func iflowSessionIDFromPath(root, path string) string {
	if !isIflowSourcePath(root, path) {
		return ""
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return strings.TrimPrefix(stem, "session-")
}

func iflowProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage: CapabilitySupported,
			Thinking:     CapabilitySupported,
			ToolCalls:    CapabilitySupported,
			ToolResults:  CapabilitySupported,
		},
	}
}
