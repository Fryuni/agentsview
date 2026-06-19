package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*commandCodeProvider)(nil)

type commandCodeProviderFactory struct {
	def AgentDef
}

func newCommandCodeProviderFactory(def AgentDef) ProviderFactory {
	return commandCodeProviderFactory{def: cloneAgentDef(def)}
}

func (f commandCodeProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f commandCodeProviderFactory) Capabilities() Capabilities {
	return commandCodeProviderCapabilities()
}

func (f commandCodeProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &commandCodeProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   commandCodeProviderCapabilities(),
			Config: cfg,
		},
		sources: newCommandCodeSourceSet(cfg.Roots),
	}
}

type commandCodeProvider struct {
	ProviderBase
	sources DirectoryJSONLSourceSet
}

func (p *commandCodeProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *commandCodeProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *commandCodeProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *commandCodeProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	return p.sources.FindSource(ctx, providerFindRequestWithRawSessionID(p.Def, req))
}

func (p *commandCodeProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *commandCodeProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("commandcode source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseCommandCodeSession(path, machine)
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

func newCommandCodeSourceSet(roots []string) DirectoryJSONLSourceSet {
	return NewDirectoryJSONLSourceSet(
		AgentCommandCode,
		roots,
		JSONLSourceSetOptions{
			FollowSymlinkDirs: true,
			IncludePath:       isCommandCodeSourcePath,
			ProjectHint:       func(root, path string) string { return "" },
			SessionIDFromPath: commandCodeSessionIDFromPath,
		},
	)
}

func isCommandCodeSourcePath(root, path string) bool {
	name := filepath.Base(path)
	if !strings.HasSuffix(name, ".jsonl") ||
		strings.HasSuffix(name, ".checkpoints.jsonl") ||
		strings.HasSuffix(name, ".prompts.jsonl") {
		return false
	}
	return IsValidSessionID(strings.TrimSuffix(name, ".jsonl"))
}

func commandCodeSessionIDFromPath(root, path string) string {
	name := filepath.Base(path)
	if !isCommandCodeSourcePath(root, path) {
		return ""
	}
	return strings.TrimSuffix(name, ".jsonl")
}

func commandCodeProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage:       CapabilitySupported,
			SessionName:        CapabilitySupported,
			Cwd:                CapabilitySupported,
			GitBranch:          CapabilitySupported,
			Thinking:           CapabilitySupported,
			ToolCalls:          CapabilitySupported,
			ToolResults:        CapabilitySupported,
			MalformedLineCount: CapabilitySupported,
		},
	}
}
