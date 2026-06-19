package parser

import (
	"context"
	"fmt"
	"path/filepath"
)

var _ Provider = (*ampProvider)(nil)

type ampProviderFactory struct {
	def AgentDef
}

func newAmpProviderFactory(def AgentDef) ProviderFactory {
	return ampProviderFactory{def: cloneAgentDef(def)}
}

func (f ampProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f ampProviderFactory) Capabilities() Capabilities {
	return ampProviderCapabilities()
}

func (f ampProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &ampProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   ampProviderCapabilities(),
			Config: cfg,
		},
		sources: newAmpSourceSet(cfg.Roots),
	}
}

type ampProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *ampProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *ampProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *ampProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *ampProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	return p.sources.FindSource(ctx, providerFindRequestWithRawSessionID(p.Def, req))
}

func (p *ampProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *ampProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("amp source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseAmpSession(path, machine)
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

func newAmpSourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentAmp, roots, JSONLSourceSetOptions{
		Extensions:  []string{".json"},
		IncludePath: isAmpSourcePath,
		SessionIDFromPath: func(root, path string) string {
			return ampThreadIDFromPath(path)
		},
	})
}

func isAmpSourcePath(root, path string) bool {
	return IsAmpThreadFileName(filepath.Base(path))
}

func ampProviderCapabilities() Capabilities {
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
