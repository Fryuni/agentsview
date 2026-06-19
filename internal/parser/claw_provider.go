package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	_ Provider = (*openClawProvider)(nil)
	_ Provider = (*qClawProvider)(nil)
)

type clawProviderSpec struct {
	agent       AgentType
	discover    func(string) []DiscoveredFile
	find        func(string, string) string
	parse       func(string, string, string) (*ParsedSession, []ParsedMessage, error)
	sessionFile func(string) bool
	sessionID   func(string) string
}

type openClawProviderFactory struct {
	def AgentDef
}

func newOpenClawProviderFactory(def AgentDef) ProviderFactory {
	return openClawProviderFactory{def: cloneAgentDef(def)}
}

func (f openClawProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f openClawProviderFactory) Capabilities() Capabilities {
	return openClawProviderCapabilities()
}

func (f openClawProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &openClawProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   openClawProviderCapabilities(),
			Config: cfg,
		},
		sources: newClawSourceSet(cfg.Roots, openClawProviderSpec()),
	}
}

type openClawProvider struct {
	ProviderBase
	sources clawSourceSet
}

func (p *openClawProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *openClawProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *openClawProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *openClawProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *openClawProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *openClawProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	return parseClawSource(ctx, p.sources, req, p.Config.Machine)
}

type qClawProviderFactory struct {
	def AgentDef
}

func newQClawProviderFactory(def AgentDef) ProviderFactory {
	return qClawProviderFactory{def: cloneAgentDef(def)}
}

func (f qClawProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f qClawProviderFactory) Capabilities() Capabilities {
	return qClawProviderCapabilities()
}

func (f qClawProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &qClawProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   qClawProviderCapabilities(),
			Config: cfg,
		},
		sources: newClawSourceSet(cfg.Roots, qClawProviderSpec()),
	}
}

type qClawProvider struct {
	ProviderBase
	sources clawSourceSet
}

func (p *qClawProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *qClawProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *qClawProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *qClawProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *qClawProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *qClawProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	return parseClawSource(ctx, p.sources, req, p.Config.Machine)
}

type clawSource struct {
	Root string
	Path string
}

type clawSourceSet struct {
	roots []string
	spec  clawProviderSpec
}

func newClawSourceSet(roots []string, spec clawProviderSpec) clawSourceSet {
	return clawSourceSet{
		roots: cleanJSONLRoots(roots),
		spec:  spec,
	}
}

func (s clawSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range s.spec.discover(root) {
			source, ok := s.sourceRef(root, file.Path)
			if !ok {
				continue
			}
			key := string(source.Provider) + "\x00" + source.Key
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			sources = append(sources, source)
		}
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].DisplayPath != sources[j].DisplayPath {
			return sources[i].DisplayPath < sources[j].DisplayPath
		}
		return sources[i].Key < sources[j].Key
	})
	return sources, nil
}

func (s clawSourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots))
	for _, root := range s.roots {
		roots = append(roots, WatchRoot{
			Path:         root,
			Recursive:    true,
			IncludeGlobs: []string{"*.jsonl", "*.jsonl.*"},
			DebounceKey:  string(s.spec.agent) + ":claw:" + root,
		})
	}
	return WatchPlan{Roots: roots}, nil
}

func (s clawSourceSet) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.WatchRoot != "" {
		for _, root := range s.roots {
			if !samePath(root, req.WatchRoot) {
				continue
			}
			source, ok := s.sourceForChangedPathInRoot(root, req)
			if !ok {
				return nil, nil
			}
			return []SourceRef{source}, nil
		}
		return nil, nil
	}
	source, ok := s.sourceForChangedPath(req)
	if !ok {
		return nil, nil
	}
	return []SourceRef{source}, nil
}

func (s clawSourceSet) FindSource(
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
		if source, ok := s.sourceForStoredPath(path); ok {
			return source, true, nil
		}
	}
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	for _, root := range s.roots {
		path := s.spec.find(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s clawSourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	path, ok := s.pathFromSource(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("%s source path unavailable", s.spec.agent)
	}
	info, err := os.Stat(path)
	if err != nil {
		return SourceFingerprint{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return SourceFingerprint{}, fmt.Errorf("stat %s: source is a directory", path)
	}
	return SourceFingerprint{
		Key:     firstNonEmptyJSONLString(source.FingerprintKey, source.Key, path),
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
	}, nil
}

func (s clawSourceSet) pathFromSource(source SourceRef) (string, bool) {
	switch src := source.Opaque.(type) {
	case clawSource:
		return src.Path, src.Path != ""
	case *clawSource:
		if src != nil && src.Path != "" {
			return src.Path, true
		}
	}
	for _, candidate := range []string{
		source.DisplayPath,
		source.FingerprintKey,
		source.Key,
	} {
		if ref, ok := s.sourceForPath(candidate); ok {
			src := ref.Opaque.(clawSource)
			return src.Path, true
		}
	}
	return "", false
}

func (s clawSourceSet) sourceForPath(path string) (SourceRef, bool) {
	for _, root := range s.roots {
		if source, ok := s.sourceForPathInRoot(root, path); ok {
			return source, true
		}
	}
	return SourceRef{}, false
}

func (s clawSourceSet) sourceForChangedPath(req ChangedPathRequest) (SourceRef, bool) {
	for _, root := range s.roots {
		if source, ok := s.sourceForChangedPathInRoot(root, req); ok {
			return source, true
		}
	}
	return SourceRef{}, false
}

func (s clawSourceSet) sourceForChangedPathInRoot(
	root string,
	req ChangedPathRequest,
) (SourceRef, bool) {
	if source, ok := s.sourceForPathInRoot(root, req.Path); ok {
		return source, true
	}
	if !jsonlMissingPathFallbackAllowed(req) {
		return SourceRef{}, false
	}
	return s.sourceForStoredPathInRoot(root, req.Path)
}

func (s clawSourceSet) sourceForStoredPath(path string) (SourceRef, bool) {
	for _, root := range s.roots {
		if source, ok := s.sourceForStoredPathInRoot(root, path); ok {
			return source, true
		}
	}
	return SourceRef{}, false
}

func (s clawSourceSet) sourceForStoredPathInRoot(
	root string,
	path string,
) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rawID, ok := s.rawIDFromPath(root, path)
	if !ok {
		return SourceRef{}, false
	}
	best := s.spec.find(root, rawID)
	if best == "" {
		return SourceRef{}, false
	}
	return s.sourceRef(root, best)
}

func (s clawSourceSet) sourceForPathInRoot(root string, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rawID, ok := s.rawIDFromPath(root, path)
	if !ok {
		return SourceRef{}, false
	}
	best := s.spec.find(root, rawID)
	if best == "" || !samePath(best, path) {
		return SourceRef{}, false
	}
	return s.sourceRef(root, best)
}

func (s clawSourceSet) sourceRef(root string, path string) (SourceRef, bool) {
	rawID, ok := s.rawIDFromPath(root, path)
	if !ok {
		return SourceRef{}, false
	}
	return SourceRef{
		Provider:       s.spec.agent,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		ProjectHint:    clawAgentIDFromRawID(rawID),
		Opaque: clawSource{
			Root: filepath.Clean(root),
			Path: filepath.Clean(path),
		},
	}, true
}

func (s clawSourceSet) rawIDFromPath(root string, path string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[1] != "sessions" {
		return "", false
	}
	if !IsValidSessionID(parts[0]) || !s.spec.sessionFile(parts[2]) {
		return "", false
	}
	sessionID := s.spec.sessionID(parts[2])
	if !IsValidSessionID(sessionID) {
		return "", false
	}
	return parts[0] + ":" + sessionID, true
}

func parseClawSource(
	ctx context.Context,
	sources clawSourceSet,
	req ParseRequest,
	defaultMachine string,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("%s source path unavailable", sources.spec.agent)
	}
	machine := firstNonEmptyJSONLString(req.Machine, defaultMachine)
	sess, msgs, err := sources.spec.parse(path, "", machine)
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

func openClawProviderSpec() clawProviderSpec {
	return clawProviderSpec{
		agent:       AgentOpenClaw,
		discover:    DiscoverOpenClawSessions,
		find:        FindOpenClawSourceFile,
		parse:       ParseOpenClawSession,
		sessionFile: IsOpenClawSessionFile,
		sessionID:   OpenClawSessionID,
	}
}

func qClawProviderSpec() clawProviderSpec {
	return clawProviderSpec{
		agent:       AgentQClaw,
		discover:    DiscoverQClawSessions,
		find:        FindQClawSourceFile,
		parse:       ParseQClawSession,
		sessionFile: IsQClawSessionFile,
		sessionID:   QClawSessionID,
	}
}

func clawAgentIDFromRawID(rawID string) string {
	agentID, _, ok := strings.Cut(rawID, ":")
	if !ok {
		return ""
	}
	return agentID
}

func openClawProviderCapabilities() Capabilities {
	return clawProviderCapabilities()
}

func qClawProviderCapabilities() Capabilities {
	return clawProviderCapabilities()
}

func clawProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
