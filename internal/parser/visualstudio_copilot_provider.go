package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var _ Provider = (*visualStudioCopilotProvider)(nil)

type visualStudioCopilotProviderFactory struct {
	def AgentDef
}

func newVisualStudioCopilotProviderFactory(def AgentDef) ProviderFactory {
	return visualStudioCopilotProviderFactory{def: cloneAgentDef(def)}
}

func (f visualStudioCopilotProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f visualStudioCopilotProviderFactory) Capabilities() Capabilities {
	return visualStudioCopilotProviderCapabilities()
}

func (f visualStudioCopilotProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &visualStudioCopilotProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   visualStudioCopilotProviderCapabilities(),
			Config: cfg,
		},
		sources: newVisualStudioCopilotSourceSet(cfg.Roots),
	}
}

type visualStudioCopilotProvider struct {
	ProviderBase
	sources visualStudioCopilotSourceSet
}

func (p *visualStudioCopilotProvider) Discover(
	ctx context.Context,
) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *visualStudioCopilotProvider) WatchPlan(
	ctx context.Context,
) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *visualStudioCopilotProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *visualStudioCopilotProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *visualStudioCopilotProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *visualStudioCopilotProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	src, ok := p.sources.sourceFromRef(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("visual studio copilot source path unavailable")
	}
	conversationIDs := []string(nil)
	if src.ConversationID != "" {
		conversationIDs = []string{src.ConversationID}
	} else {
		ids, err := VisualStudioCopilotFileConversationIDs(src.TracePath)
		if err != nil {
			return ParseOutcome{}, err
		}
		conversationIDs = ids
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	var results []ParseResultOutcome
	for _, conversationID := range conversationIDs {
		sess, msgs, err := ParseVisualStudioCopilotConversation(
			src.TracePath,
			conversationID,
			"visualstudio",
			machine,
		)
		if err != nil {
			return ParseOutcome{}, err
		}
		if sess == nil {
			continue
		}
		if req.Fingerprint.Hash != "" {
			sess.File.Hash = req.Fingerprint.Hash
		}
		results = append(results, ParseResultOutcome{
			Result: ParseResult{
				Session:  *sess,
				Messages: msgs,
			},
			DataVersion: DataVersionCurrent,
		})
	}
	if len(results) == 0 {
		return ParseOutcome{
			ResultSetComplete: true,
			ForceReplace:      true,
			SkipReason:        SkipNoSession,
		}, nil
	}
	return ParseOutcome{
		Results:           results,
		ResultSetComplete: true,
		ForceReplace:      true,
	}, nil
}

type visualStudioCopilotSource struct {
	Root           string
	Path           string
	TracePath      string
	ConversationID string
}

type visualStudioCopilotSourceSet struct {
	roots []string
}

func newVisualStudioCopilotSourceSet(roots []string) visualStudioCopilotSourceSet {
	return visualStudioCopilotSourceSet{roots: cleanJSONLRoots(roots)}
}

func (s visualStudioCopilotSourceSet) Discover(
	ctx context.Context,
) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverVisualStudioCopilotSessions(root) {
			source, ok := s.sourceRef(root, file.Path)
			if !ok {
				continue
			}
			source.ProjectHint = file.Project
			addJSONLSource(source, &sources, seen)
		}
	}
	sortJSONLSources(sources)
	return sources, nil
}

func (s visualStudioCopilotSourceSet) WatchPlan(
	context.Context,
) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots))
	for _, root := range s.roots {
		roots = append(roots, WatchRoot{
			Path:         root,
			Recursive:    false,
			IncludeGlobs: []string{"*_VSGitHubCopilot_traces.jsonl"},
			DebounceKey:  string(AgentVSCopilot) + ":traces:" + root,
		})
	}
	return WatchPlan{Roots: roots}, nil
}

func (s visualStudioCopilotSourceSet) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, root := range s.roots {
		source, ok := s.sourceRefForChangedPath(root, req.Path)
		if ok {
			return []SourceRef{source}, nil
		}
	}
	return nil, nil
}

func (s visualStudioCopilotSourceSet) FindSource(
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
		for _, root := range s.roots {
			if source, ok := s.sourceRef(root, path); ok {
				return source, true, nil
			}
		}
	}
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	for _, root := range s.roots {
		path := FindVisualStudioCopilotSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s visualStudioCopilotSourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	src, ok := s.sourceFromRef(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("visual studio copilot source path unavailable")
	}
	size, mtime, err := VisualStudioCopilotTraceFingerprintStrict(src.TracePath)
	if err != nil {
		return SourceFingerprint{}, err
	}
	hash, err := hashJSONLSourceFile(src.TracePath)
	if err != nil {
		return SourceFingerprint{}, err
	}
	return SourceFingerprint{
		Key:     firstNonEmptyJSONLString(source.FingerprintKey, source.Key, src.Path),
		Size:    size,
		MTimeNS: mtime,
		Hash:    hash,
	}, nil
}

func (s visualStudioCopilotSourceSet) sourceFromRef(
	source SourceRef,
) (visualStudioCopilotSource, bool) {
	switch src := source.Opaque.(type) {
	case visualStudioCopilotSource:
		return src, src.TracePath != ""
	case *visualStudioCopilotSource:
		if src != nil && src.TracePath != "" {
			return *src, true
		}
	}
	for _, candidate := range []string{source.DisplayPath, source.FingerprintKey, source.Key} {
		for _, root := range s.roots {
			if ref, ok := s.sourceRef(root, candidate); ok {
				src := ref.Opaque.(visualStudioCopilotSource)
				return src, true
			}
		}
	}
	return visualStudioCopilotSource{}, false
}

func (s visualStudioCopilotSourceSet) sourceRef(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if tracePath, conversationID, ok :=
		ParseVisualStudioCopilotVirtualPath(path); ok {
		if !visualStudioCopilotTraceUnderRoot(root, tracePath, true) {
			return SourceRef{}, false
		}
		return s.newSourceRef(root, path, tracePath, conversationID), true
	}
	if !visualStudioCopilotTraceUnderRoot(root, path, true) {
		return SourceRef{}, false
	}
	return s.newSourceRef(root, path, path, ""), true
}

func (s visualStudioCopilotSourceSet) sourceRefForChangedPath(
	root, path string,
) (SourceRef, bool) {
	if source, ok := s.sourceRef(root, path); ok {
		return source, true
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !visualStudioCopilotTraceUnderRoot(root, path, false) {
		return SourceRef{}, false
	}
	return s.newSourceRef(root, path, path, ""), true
}

func visualStudioCopilotTraceUnderRoot(
	root, path string,
	requireRegular bool,
) bool {
	rel, ok := relUnder(root, path)
	if !ok || strings.Contains(filepath.ToSlash(rel), "/") {
		return false
	}
	if !IsVisualStudioCopilotTraceFile(path) {
		return false
	}
	return !requireRegular || IsRegularFile(path)
}

func (s visualStudioCopilotSourceSet) newSourceRef(
	root, path, tracePath, conversationID string,
) SourceRef {
	return SourceRef{
		Provider:       AgentVSCopilot,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		ProjectHint:    "visualstudio",
		Opaque: visualStudioCopilotSource{
			Root:           root,
			Path:           path,
			TracePath:      tracePath,
			ConversationID: conversationID,
		},
	}
}

func visualStudioCopilotProviderCapabilities() Capabilities {
	return Capabilities{
		Source: SourceCapabilities{
			DiscoverSources:      CapabilitySupported,
			WatchSources:         CapabilitySupported,
			ClassifyChangedPath:  CapabilitySupported,
			FindSource:           CapabilitySupported,
			CompositeFingerprint: CapabilitySupported,
			IncrementalAppend:    CapabilityNotApplicable,
			MultiSessionSource:   CapabilitySupported,
			PerSessionErrors:     CapabilityNotApplicable,
			ExcludedSessions:     CapabilityNotApplicable,
			ForceReplaceOnParse:  CapabilitySupported,
		},
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
