package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*zencoderProvider)(nil)

type zencoderProviderFactory struct {
	def AgentDef
}

func newZencoderProviderFactory(def AgentDef) ProviderFactory {
	return zencoderProviderFactory{def: cloneAgentDef(def)}
}

func (f zencoderProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f zencoderProviderFactory) Capabilities() Capabilities {
	return zencoderProviderCapabilities()
}

func (f zencoderProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &zencoderProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   zencoderProviderCapabilities(),
			Config: cfg,
		},
		sources: newZencoderSourceSet(cfg.Roots),
	}
}

type zencoderProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *zencoderProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *zencoderProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *zencoderProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *zencoderProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	return p.sources.FindSource(ctx, providerFindRequestWithRawSessionID(p.Def, req))
}

func (p *zencoderProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *zencoderProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("zencoder source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseZencoderSession(path, machine)
	if err != nil {
		return ParseOutcome{}, err
	}
	if sess == nil {
		return ParseOutcome{
			ResultSetComplete: true,
			SkipReason:        SkipNoSession,
		}, nil
	}
	if req.Fingerprint.Hash != "" {
		sess.File.Hash = req.Fingerprint.Hash
	}
	return ParseOutcome{
		Results: []ParseResultOutcome{{
			Result: ParseResult{
				Session:  *sess,
				Messages: msgs,
			},
			DataVersion: DataVersionCurrent,
		}},
		ResultSetComplete: true,
	}, nil
}

func newZencoderSourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentZencoder, roots, JSONLSourceSetOptions{
		IncludePath:       isZencoderSourcePath,
		SessionIDFromPath: zencoderSessionIDFromPath,
	})
}

func isZencoderSourcePath(root, path string) bool {
	return IsZencoderSessionFileName(filepath.Base(path))
}

func zencoderSessionIDFromPath(root, path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

func zencoderProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage:  CapabilitySupported,
			Cwd:           CapabilitySupported,
			Relationships: CapabilitySupported,
			Subagents:     CapabilitySupported,
			Thinking:      CapabilitySupported,
			ToolCalls:     CapabilitySupported,
			ToolResults:   CapabilitySupported,
		},
	}
}
