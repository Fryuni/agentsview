package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*qwenProvider)(nil)

type qwenProviderFactory struct {
	def AgentDef
}

func newQwenProviderFactory(def AgentDef) ProviderFactory {
	return qwenProviderFactory{def: cloneAgentDef(def)}
}

func (f qwenProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f qwenProviderFactory) Capabilities() Capabilities {
	return qwenProviderCapabilities()
}

func (f qwenProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &qwenProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   qwenProviderCapabilities(),
			Config: cfg,
		},
		sources: newQwenSourceSet(cfg.Roots),
	}
}

type qwenProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *qwenProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *qwenProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *qwenProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *qwenProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	return p.sources.FindSource(ctx, providerFindRequestWithRawSessionID(p.Def, req))
}

func (p *qwenProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *qwenProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("qwen source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	project := req.Source.ProjectHint
	sess, msgs, err := ParseQwenSession(path, project, machine)
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

func newQwenSourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentQwen, roots, JSONLSourceSetOptions{
		Recursive:          true,
		FollowSymlinkDirs:  true,
		FollowSymlinkFiles: true,
		IncludePath:        isQwenSourcePath,
		ProjectHint:        qwenProjectHintFromPath,
		SessionIDFromPath:  qwenSessionIDFromPath,
	})
}

func isQwenSourcePath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 3 &&
		parts[0] != "" && parts[0] != "." && parts[0] != ".." &&
		parts[1] == "chats" &&
		parts[2] != "" && parts[2] != "." && parts[2] != ".." &&
		strings.HasSuffix(parts[2], ".jsonl")
}

func qwenProjectHintFromPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 {
		return ""
	}
	return GetProjectName(parts[0])
}

func qwenSessionIDFromPath(root, path string) string {
	if !isQwenSourcePath(root, path) {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

func qwenProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			Cwd:                  CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
