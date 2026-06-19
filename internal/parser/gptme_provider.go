package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type gptmeProviderFactory struct {
	def AgentDef
}

func newGptmeProviderFactory(def AgentDef) ProviderFactory {
	return gptmeProviderFactory{def: cloneAgentDef(def)}
}

func (f gptmeProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f gptmeProviderFactory) Capabilities() Capabilities {
	return gptmeProviderCapabilities()
}

func (f gptmeProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &gptmeProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   gptmeProviderCapabilities(),
			Config: cfg,
		},
		sources: newGptmeSourceSet(cfg.Roots),
	}
}

type gptmeProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *gptmeProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	sources, err := p.sources.Discover(ctx)
	if err != nil {
		return nil, err
	}
	return p.filterSources(sources), nil
}

func (p *gptmeProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *gptmeProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	sources, err := p.sources.SourcesForChangedPath(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.filterSources(sources), nil
}

func (p *gptmeProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	if err := ctx.Err(); err != nil {
		return SourceRef{}, false, err
	}
	if req.StoredFilePath != "" {
		source, ok := p.sources.sourceForPath(req.StoredFilePath)
		if ok && p.isSource(source) {
			return source, true, nil
		}
	}
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	for _, root := range p.Config.Roots {
		path := filepath.Join(root, req.RawSessionID, "conversation.jsonl")
		source, ok := p.sources.sourceForPath(path)
		if ok && p.isSource(source) {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (p *gptmeProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *gptmeProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("gptme source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseGptmeSession(path, machine)
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

func (p *gptmeProvider) filterSources(sources []SourceRef) []SourceRef {
	if len(sources) == 0 {
		return nil
	}
	filtered := sources[:0]
	for _, source := range sources {
		if p.isSource(source) {
			filtered = append(filtered, source)
		}
	}
	return filtered
}

func (p *gptmeProvider) isSource(source SourceRef) bool {
	src, ok := source.Opaque.(JSONLSource)
	if !ok {
		return false
	}
	return isGptmeConversationPath(src.Root, src.Path)
}

func newGptmeSourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentGptme, roots, JSONLSourceSetOptions{
		Recursive: true,
		Hash:      true,
		Include: func(path string, info os.FileInfo) bool {
			return !info.IsDir() && filepath.Base(path) == "conversation.jsonl"
		},
		ProjectHint: func(root, path string) string {
			sessionID := gptmeSessionIDFromPath(root, path)
			if sessionID == "" {
				return ""
			}
			return gptmeProjectFromSessionName(sessionID)
		},
		SessionIDFromPath: gptmeSessionIDFromPath,
	})
}

func gptmeProviderCapabilities() Capabilities {
	return Capabilities{
		Source: SourceCapabilities{
			DiscoverSources:      CapabilitySupported,
			WatchSources:         CapabilitySupported,
			ClassifyChangedPath:  CapabilitySupported,
			FindSource:           CapabilitySupported,
			CompositeFingerprint: CapabilitySupported,
			MultiSessionSource:   CapabilityNotApplicable,
			PerSessionErrors:     CapabilityNotApplicable,
			ExcludedSessions:     CapabilityNotApplicable,
			ForceReplaceOnParse:  CapabilityNotApplicable,
		},
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			Model:                CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
		},
	}
}

func isGptmeConversationPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 2 && parts[1] == "conversation.jsonl" &&
		parts[0] != "." && parts[0] != ".." && parts[0] != ""
}

func gptmeSessionIDFromPath(root, path string) string {
	if !isGptmeConversationPath(root, path) {
		return ""
	}
	return filepath.Base(filepath.Dir(path))
}
