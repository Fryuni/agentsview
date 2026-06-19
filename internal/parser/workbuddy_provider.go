package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*workBuddyProvider)(nil)

type workBuddyProviderFactory struct {
	def AgentDef
}

func newWorkBuddyProviderFactory(def AgentDef) ProviderFactory {
	return workBuddyProviderFactory{def: cloneAgentDef(def)}
}

func (f workBuddyProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f workBuddyProviderFactory) Capabilities() Capabilities {
	return workBuddyProviderCapabilities()
}

func (f workBuddyProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &workBuddyProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   workBuddyProviderCapabilities(),
			Config: cfg,
		},
		sources: newWorkBuddySourceSet(cfg.Roots),
	}
}

type workBuddyProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *workBuddyProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *workBuddyProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *workBuddyProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *workBuddyProvider) FindSource(
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
	for _, root := range p.Config.Roots {
		path := FindWorkBuddySourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := p.sources.sourceForPath(path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (p *workBuddyProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *workBuddyProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("workbuddy source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseWorkBuddySession(path, req.Source.ProjectHint, machine)
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

func newWorkBuddySourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentWorkBuddy, roots, JSONLSourceSetOptions{
		Recursive:          true,
		FollowSymlinkDirs:  true,
		FollowSymlinkFiles: true,
		IncludePath:        isWorkBuddySourcePath,
		ProjectHint:        workBuddyProjectHintFromPath,
		SessionIDFromPath:  workBuddySessionIDFromPath,
	})
}

func isWorkBuddySourcePath(root, path string) bool {
	parts, ok := workBuddyPathParts(root, path)
	if !ok {
		return false
	}
	switch len(parts) {
	case 2:
		stem, ok := strings.CutSuffix(parts[1], ".jsonl")
		return ok && IsValidSessionID(stem)
	case 4:
		return IsValidSessionID(parts[1]) &&
			parts[2] == "subagents" &&
			strings.HasSuffix(parts[3], ".jsonl")
	default:
		return false
	}
}

func workBuddyProjectHintFromPath(root, path string) string {
	parts, ok := workBuddyPathParts(root, path)
	if !ok || len(parts) < 2 {
		return ""
	}
	return parts[0]
}

func workBuddySessionIDFromPath(root, path string) string {
	if !isWorkBuddySourcePath(root, path) {
		return ""
	}
	parts, _ := workBuddyPathParts(root, path)
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if len(parts) == 4 {
		return parts[1] + ":subagent:" + stem
	}
	return stem
}

func workBuddyPathParts(root, path string) ([]string, bool) {
	rel, err := filepath.Rel(root, path)
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

func workBuddyProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			Cwd:                  CapabilitySupported,
			Relationships:        CapabilitySupported,
			Subagents:            CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			MalformedLineCount:   CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
