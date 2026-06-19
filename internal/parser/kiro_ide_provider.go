package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*kiroIDEProvider)(nil)

type kiroIDEProviderFactory struct {
	def AgentDef
}

func newKiroIDEProviderFactory(def AgentDef) ProviderFactory {
	return kiroIDEProviderFactory{def: cloneAgentDef(def)}
}

func (f kiroIDEProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f kiroIDEProviderFactory) Capabilities() Capabilities {
	return kiroIDEProviderCapabilities()
}

func (f kiroIDEProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &kiroIDEProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   kiroIDEProviderCapabilities(),
			Config: cfg,
		},
		sources: newKiroIDESourceSet(cfg.Roots),
	}
}

type kiroIDEProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *kiroIDEProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *kiroIDEProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *kiroIDEProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *kiroIDEProvider) FindSource(
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
		path := FindKiroIDESourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := p.sources.sourceForPath(path); ok {
			return source, true, nil
		}
	}
	return p.sources.FindSource(ctx, req)
}

func (p *kiroIDEProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *kiroIDEProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("kiro ide source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseKiroIDESession(path, machine)
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

func newKiroIDESourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentKiroIDE, roots, JSONLSourceSetOptions{
		Recursive:         true,
		Extensions:        []string{".chat", ".json"},
		Hash:              true,
		IncludePath:       isKiroIDESourcePath,
		SessionIDFromPath: kiroIDESessionIDFromPath,
	})
}

func isKiroIDESourcePath(root, path string) bool {
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	if len(parts) == 2 {
		if parts[0] == "default" || parts[0] == "dev_data" ||
			parts[0] == "index" || parts[0] == "workspace-sessions" ||
			strings.HasPrefix(parts[0], ".") {
			return false
		}
		return strings.HasSuffix(parts[1], ".chat")
	}
	return len(parts) == 3 &&
		parts[0] == "workspace-sessions" &&
		!strings.HasPrefix(parts[1], ".") &&
		parts[2] != "sessions.json" &&
		strings.HasSuffix(parts[2], ".json")
}

func kiroIDESessionIDFromPath(root, path string) string {
	if !isKiroIDESourcePath(root, path) {
		return ""
	}
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 2 {
		return parts[0] + ":" + strings.TrimSuffix(parts[1], ".chat")
	}
	if len(parts) == 3 {
		return strings.TrimSuffix(parts[2], ".json")
	}
	return ""
}

func kiroIDEProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage: CapabilitySupported,
			SessionName:  CapabilitySupported,
			ToolCalls:    CapabilitySupported,
			Model:        CapabilitySupported,
		},
	}
}
