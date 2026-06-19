package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var _ Provider = (*vscodeCopilotProvider)(nil)

type vscodeCopilotProviderFactory struct {
	def AgentDef
}

func newVSCodeCopilotProviderFactory(def AgentDef) ProviderFactory {
	return vscodeCopilotProviderFactory{def: cloneAgentDef(def)}
}

func (f vscodeCopilotProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f vscodeCopilotProviderFactory) Capabilities() Capabilities {
	return vscodeCopilotProviderCapabilities()
}

func (f vscodeCopilotProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &vscodeCopilotProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   vscodeCopilotProviderCapabilities(),
			Config: cfg,
		},
		sources: newVSCodeCopilotSourceSet(cfg.Roots),
	}
}

type vscodeCopilotProvider struct {
	ProviderBase
	sources vscodeCopilotSourceSet
}

func (p *vscodeCopilotProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *vscodeCopilotProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *vscodeCopilotProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *vscodeCopilotProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *vscodeCopilotProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *vscodeCopilotProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, project, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("vscode copilot source path unavailable")
	}
	if req.Source.ProjectHint != "" {
		project = req.Source.ProjectHint
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseVSCodeCopilotSession(path, project, machine)
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

type vscodeCopilotSource struct {
	Root    string
	Path    string
	Project string
}

type vscodeCopilotSourceSet struct {
	roots []string
}

func newVSCodeCopilotSourceSet(roots []string) vscodeCopilotSourceSet {
	return vscodeCopilotSourceSet{roots: cleanJSONLRoots(roots)}
}

func (s vscodeCopilotSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverVSCodeCopilotSessions(root) {
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

func (s vscodeCopilotSourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots)*2)
	for _, root := range s.roots {
		workspace := filepath.Join(root, "workspaceStorage")
		roots = append(roots, WatchRoot{
			Path:         workspace,
			Recursive:    true,
			IncludeGlobs: []string{"*.json", "*.jsonl"},
			DebounceKey:  string(AgentVSCodeCopilot) + ":workspace:" + workspace,
		})
		global := filepath.Join(root, "globalStorage")
		roots = append(roots, WatchRoot{
			Path:         global,
			Recursive:    true,
			IncludeGlobs: []string{"*.json", "*.jsonl"},
			DebounceKey:  string(AgentVSCodeCopilot) + ":global:" + global,
		})
	}
	return WatchPlan{Roots: roots}, nil
}

func (s vscodeCopilotSourceSet) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, root := range s.roots {
		sources := s.sourcesForWorkspaceManifest(root, req.Path)
		if len(sources) > 0 {
			return sources, nil
		}
		source, ok := s.sourceRefForChangedPath(root, req.Path)
		if ok {
			return []SourceRef{source}, nil
		}
	}
	return nil, nil
}

func (s vscodeCopilotSourceSet) FindSource(
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
		path := FindVSCodeCopilotSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s vscodeCopilotSourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	path, _, ok := s.pathFromSource(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("vscode copilot source path unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return SourceFingerprint{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return SourceFingerprint{}, fmt.Errorf("stat %s: source is a directory", path)
	}
	hash, err := hashJSONLSourceFile(path)
	if err != nil {
		return SourceFingerprint{}, err
	}
	return SourceFingerprint{
		Key:     firstNonEmptyJSONLString(source.FingerprintKey, source.Key, path),
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
		Hash:    hash,
	}, nil
}

func (s vscodeCopilotSourceSet) pathFromSource(source SourceRef) (string, string, bool) {
	switch src := source.Opaque.(type) {
	case vscodeCopilotSource:
		return src.Path, src.Project, src.Path != ""
	case *vscodeCopilotSource:
		if src != nil && src.Path != "" {
			return src.Path, src.Project, true
		}
	}
	for _, candidate := range []string{source.DisplayPath, source.FingerprintKey, source.Key} {
		for _, root := range s.roots {
			if ref, ok := s.sourceRef(root, candidate); ok {
				src := ref.Opaque.(vscodeCopilotSource)
				return src.Path, src.Project, true
			}
		}
	}
	return "", "", false
}

func (s vscodeCopilotSourceSet) sourceRef(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, ok := relUnder(root, path)
	if !ok {
		return SourceRef{}, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 4 &&
		parts[0] == "workspaceStorage" &&
		parts[2] == "chatSessions" &&
		isVSCodeCopilotSessionPath(parts[3]) {
		if promoted := vscodeCopilotPreferredExistingPath(path); promoted != "" {
			path = promoted
		}
		if !IsRegularFile(path) {
			return SourceRef{}, false
		}
		hashDir := filepath.Join(root, "workspaceStorage", parts[1])
		project := ReadVSCodeWorkspaceManifest(hashDir)
		if project == "" {
			project = "unknown"
		}
		return s.newSourceRef(root, path, project), true
	}
	if len(parts) == 3 &&
		parts[0] == "globalStorage" &&
		(parts[1] == "emptyWindowChatSessions" ||
			parts[1] == "transferredChatSessions") &&
		isVSCodeCopilotSessionPath(parts[2]) {
		if promoted := vscodeCopilotPreferredExistingPath(path); promoted != "" {
			path = promoted
		}
		if !IsRegularFile(path) {
			return SourceRef{}, false
		}
		return s.newSourceRef(root, path, "empty-window"), true
	}
	return SourceRef{}, false
}

func (s vscodeCopilotSourceSet) sourceRefForChangedPath(
	root, path string,
) (SourceRef, bool) {
	if source, ok := s.sourceRef(root, path); ok {
		return source, true
	}
	return s.syntheticSourceRef(root, path)
}

func (s vscodeCopilotSourceSet) syntheticSourceRef(
	root, path string,
) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, ok := relUnder(root, path)
	if !ok {
		return SourceRef{}, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 4 &&
		parts[0] == "workspaceStorage" &&
		parts[2] == "chatSessions" &&
		isVSCodeCopilotSessionPath(parts[3]) {
		if promoted := vscodeCopilotPreferredExistingPath(path); promoted != "" {
			path = promoted
		}
		hashDir := filepath.Join(root, "workspaceStorage", parts[1])
		project := ReadVSCodeWorkspaceManifest(hashDir)
		if project == "" {
			project = "unknown"
		}
		return s.newSourceRef(root, path, project), true
	}
	if len(parts) == 3 &&
		parts[0] == "globalStorage" &&
		(parts[1] == "emptyWindowChatSessions" ||
			parts[1] == "transferredChatSessions") &&
		isVSCodeCopilotSessionPath(parts[2]) {
		if promoted := vscodeCopilotPreferredExistingPath(path); promoted != "" {
			path = promoted
		}
		return s.newSourceRef(root, path, "empty-window"), true
	}
	return SourceRef{}, false
}

func (s vscodeCopilotSourceSet) sourcesForWorkspaceManifest(
	root, path string,
) []SourceRef {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, ok := relUnder(root, path)
	if !ok {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 3 ||
		parts[0] != "workspaceStorage" ||
		parts[2] != "workspace.json" {
		return nil
	}
	hashDir := filepath.Join(root, "workspaceStorage", parts[1])
	chatDir := filepath.Join(hashDir, "chatSessions")
	entries, err := os.ReadDir(chatDir)
	if err != nil {
		return nil
	}
	project := ReadVSCodeWorkspaceManifest(hashDir)
	if project == "" {
		project = "unknown"
	}
	files := discoverVSCodeSessionFiles(chatDir, entries, project)
	sources := make([]SourceRef, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		source, ok := s.sourceRef(root, file.Path)
		if !ok {
			continue
		}
		source.ProjectHint = file.Project
		addJSONLSource(source, &sources, seen)
	}
	sortJSONLSources(sources)
	return sources
}

func (s vscodeCopilotSourceSet) newSourceRef(root, path, project string) SourceRef {
	return SourceRef{
		Provider:       AgentVSCodeCopilot,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		ProjectHint:    project,
		Opaque: vscodeCopilotSource{
			Root:    root,
			Path:    path,
			Project: project,
		},
	}
}

func isVSCodeCopilotSessionPath(name string) bool {
	return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonl")
}

func vscodeCopilotPreferredExistingPath(path string) string {
	if base, ok := strings.CutSuffix(path, ".json"); ok {
		candidate := base + ".jsonl"
		if IsRegularFile(candidate) {
			return candidate
		}
	}
	if IsRegularFile(path) {
		return path
	}
	if base, ok := strings.CutSuffix(path, ".jsonl"); ok {
		candidate := base + ".json"
		if IsRegularFile(candidate) {
			return candidate
		}
	}
	return ""
}

func vscodeCopilotProviderCapabilities() Capabilities {
	return Capabilities{
		Source: SourceCapabilities{
			DiscoverSources:      CapabilitySupported,
			WatchSources:         CapabilitySupported,
			ClassifyChangedPath:  CapabilitySupported,
			FindSource:           CapabilitySupported,
			CompositeFingerprint: CapabilitySupported,
			IncrementalAppend:    CapabilityNotApplicable,
			MultiSessionSource:   CapabilityNotApplicable,
			PerSessionErrors:     CapabilityNotApplicable,
			ExcludedSessions:     CapabilityNotApplicable,
			ForceReplaceOnParse:  CapabilityNotApplicable,
		},
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			Thinking:             CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
