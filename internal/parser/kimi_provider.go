package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*kimiProvider)(nil)

type kimiProviderFactory struct {
	def AgentDef
}

func newKimiProviderFactory(def AgentDef) ProviderFactory {
	return kimiProviderFactory{def: cloneAgentDef(def)}
}

func (f kimiProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f kimiProviderFactory) Capabilities() Capabilities {
	return kimiProviderCapabilities()
}

func (f kimiProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &kimiProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   kimiProviderCapabilities(),
			Config: cfg,
		},
		sources: newKimiSourceSet(cfg.Roots),
	}
}

type kimiProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *kimiProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *kimiProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *kimiProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *kimiProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	if err := ctx.Err(); err != nil {
		return SourceRef{}, false, err
	}
	if req.StoredFilePath != "" {
		if source, ok := p.sources.sourceForPath(req.StoredFilePath); ok {
			return source, true, nil
		}
	}
	if req.FingerprintKey != "" {
		if source, ok := p.sources.sourceForPath(req.FingerprintKey); ok {
			return source, true, nil
		}
	}
	req = providerFindRequestWithRawSessionID(p.Def, req)
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	for _, root := range p.sources.roots {
		path := FindKimiSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := p.sources.sourceForPath(path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (p *kimiProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *kimiProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("kimi source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	project := req.Source.ProjectHint
	sess, msgs, err := ParseKimiSession(path, project, machine)
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

func newKimiSourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentKimi, roots, JSONLSourceSetOptions{
		Recursive:          true,
		FollowSymlinkDirs:  true,
		FollowSymlinkFiles: true,
		IncludePath:        isKimiSourcePath,
		ProjectHint:        kimiProjectHintFromPath,
		SessionIDFromPath: func(root, path string) string {
			if !isKimiSourcePath(root, path) {
				return ""
			}
			return kimiSessionIDFromPath(path)
		},
	})
}

func isKimiSourcePath(root, path string) bool {
	parts, ok := kimiSourceRelParts(root, path)
	if !ok || len(parts) == 0 || parts[len(parts)-1] != "wire.jsonl" {
		return false
	}
	switch len(parts) {
	case 3:
		return kimiIDComponentsValid(parts[0], parts[1])
	case 5:
		return parts[2] == "agents" &&
			kimiIDComponentsValid(parts[0], parts[1], parts[3])
	default:
		return false
	}
}

func kimiProjectHintFromPath(root, path string) string {
	parts, ok := kimiSourceRelParts(root, path)
	if !ok || len(parts) == 0 {
		return ""
	}
	return DecodeKimiProjectDir(parts[0])
}

func kimiSourceRelParts(root, path string) ([]string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return nil, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, false
		}
	}
	return parts, true
}

func kimiProviderCapabilities() Capabilities {
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
