package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*qwenPawProvider)(nil)

type qwenPawProviderFactory struct {
	def AgentDef
}

func newQwenPawProviderFactory(def AgentDef) ProviderFactory {
	return qwenPawProviderFactory{def: cloneAgentDef(def)}
}

func (f qwenPawProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f qwenPawProviderFactory) Capabilities() Capabilities {
	return qwenPawProviderCapabilities()
}

func (f qwenPawProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &qwenPawProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   qwenPawProviderCapabilities(),
			Config: cfg,
		},
		sources: newQwenPawSourceSet(cfg.Roots),
	}
}

type qwenPawProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *qwenPawProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *qwenPawProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *qwenPawProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *qwenPawProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	if err := ctx.Err(); err != nil {
		return SourceRef{}, false, err
	}
	for _, path := range []string{req.StoredFilePath, req.FingerprintKey} {
		if path == "" {
			continue
		}
		if source, ok := p.sources.sourceForPath(path); ok {
			return source, true, nil
		}
	}
	req = providerFindRequestWithRawSessionID(p.Def, req)
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	for _, root := range p.sources.roots {
		path := FindQwenPawSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := p.sources.sourceForPath(path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (p *qwenPawProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *qwenPawProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("qwenpaw source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseQwenPawSession(path, req.Source.ProjectHint, machine)
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
		ForceReplace:      true,
	}, nil
}

func newQwenPawSourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentQwenPaw, roots, JSONLSourceSetOptions{
		Recursive:          true,
		Extensions:         []string{".json"},
		Hash:               true,
		FollowSymlinkDirs:  true,
		FollowSymlinkFiles: true,
		IncludePath:        isQwenPawSourcePath,
		ProjectHint:        qwenPawProjectHintFromPath,
		SessionIDFromPath:  qwenPawSessionIDFromPath,
	})
}

func isQwenPawSourcePath(root, path string) bool {
	parts, ok := qwenPawSourcePathParts(root, path)
	return ok && qwenPawSourcePathPartsValid(parts)
}

func qwenPawSourcePathPartsValid(parts []string) bool {
	if len(parts) < 3 || parts[1] != "sessions" {
		return false
	}
	workspace := parts[0]
	stem, ok := strings.CutSuffix(parts[len(parts)-1], ".json")
	if !ok || !IsValidQwenPawIDPart(workspace) ||
		!IsValidQwenPawIDPart(stem) {
		return false
	}
	switch len(parts) {
	case 3:
		return true
	case 4:
		subdir := parts[2]
		return !strings.HasPrefix(subdir, ".") &&
			IsValidQwenPawIDPart(subdir)
	default:
		return false
	}
}

func qwenPawProjectHintFromPath(root, path string) string {
	parts, ok := qwenPawSourcePathParts(root, path)
	if !ok || len(parts) < 3 {
		return ""
	}
	return parts[0]
}

func qwenPawSessionIDFromPath(root, path string) string {
	parts, ok := qwenPawSourcePathParts(root, path)
	if !ok || !qwenPawSourcePathPartsValid(parts) {
		return ""
	}
	stem := strings.TrimSuffix(parts[len(parts)-1], ".json")
	if len(parts) == 4 {
		return parts[0] + ":" + parts[2] + ":" + stem
	}
	return parts[0] + ":" + stem
}

func qwenPawSourcePathParts(root, path string) ([]string, bool) {
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

func qwenPawProviderCapabilities() Capabilities {
	source := jsonlFileProviderSourceCapabilities()
	source.ForceReplaceOnParse = CapabilitySupported
	return Capabilities{
		Source: source,
		Content: ContentCapabilities{
			FirstMessage:       CapabilitySupported,
			Thinking:           CapabilitySupported,
			ToolCalls:          CapabilitySupported,
			ToolResults:        CapabilitySupported,
			MalformedLineCount: CapabilitySupported,
		},
	}
}
