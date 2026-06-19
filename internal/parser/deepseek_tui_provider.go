package parser

import (
	"context"
	"fmt"
	"path/filepath"
)

var _ Provider = (*deepSeekTUIProvider)(nil)

type deepSeekTUIProviderFactory struct {
	def AgentDef
}

func newDeepSeekTUIProviderFactory(def AgentDef) ProviderFactory {
	return deepSeekTUIProviderFactory{def: cloneAgentDef(def)}
}

func (f deepSeekTUIProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f deepSeekTUIProviderFactory) Capabilities() Capabilities {
	return deepSeekTUIProviderCapabilities()
}

func (f deepSeekTUIProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &deepSeekTUIProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   deepSeekTUIProviderCapabilities(),
			Config: cfg,
		},
		sources: newDeepSeekTUISourceSet(cfg.Roots),
	}
}

type deepSeekTUIProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *deepSeekTUIProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *deepSeekTUIProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *deepSeekTUIProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *deepSeekTUIProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	return p.sources.FindSource(ctx, providerFindRequestWithRawSessionID(p.Def, req))
}

func (p *deepSeekTUIProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *deepSeekTUIProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("deepseek-tui source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseDeepSeekTUISession(path, machine)
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

func newDeepSeekTUISourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentDeepSeekTUI, roots, JSONLSourceSetOptions{
		Extensions:  []string{".json"},
		IncludePath: isDeepSeekTUISourcePath,
		SessionIDFromPath: func(root, path string) string {
			return deepSeekTUISessionIDFromPath(path)
		},
	})
}

func isDeepSeekTUISourcePath(root, path string) bool {
	return isDeepSeekTUISessionFile(filepath.Base(path))
}

func deepSeekTUIProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage: CapabilitySupported,
			SessionName:  CapabilitySupported,
			Cwd:          CapabilitySupported,
			Model:        CapabilitySupported,
			Thinking:     CapabilitySupported,
			ToolCalls:    CapabilitySupported,
			ToolResults:  CapabilitySupported,
		},
	}
}
