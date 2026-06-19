package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var _ Provider = (*vibeProvider)(nil)

type vibeProviderFactory struct {
	def AgentDef
}

func newVibeProviderFactory(def AgentDef) ProviderFactory {
	return vibeProviderFactory{def: cloneAgentDef(def)}
}

func (f vibeProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f vibeProviderFactory) Capabilities() Capabilities {
	return vibeProviderCapabilities()
}

func (f vibeProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &vibeProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   vibeProviderCapabilities(),
			Config: cfg,
		},
		sources: newVibeSourceSet(cfg.Roots),
	}
}

type vibeProvider struct {
	ProviderBase
	sources vibeSourceSet
}

func (p *vibeProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *vibeProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *vibeProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *vibeProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *vibeProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *vibeProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("vibe source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, usageEvents, err := ParseVibeSessionWrapper(path, "", machine)
	if err != nil {
		return ParseOutcome{}, err
	}
	if sess == nil {
		return ParseOutcome{
			ResultSetComplete: true,
			SkipReason:        SkipNoSession,
		}, nil
	}
	if req.Fingerprint.Size > 0 {
		sess.File.Size = req.Fingerprint.Size
	}
	if req.Fingerprint.MTimeNS > 0 {
		sess.File.Mtime = req.Fingerprint.MTimeNS
	}
	if req.Fingerprint.Hash != "" {
		sess.File.Hash = req.Fingerprint.Hash
	}

	excluded := vibeProviderExcludedSessionIDs(path, sess.ID)
	return ParseOutcome{
		Results: []ParseResultOutcome{{
			Result: ParseResult{
				Session:     *sess,
				Messages:    msgs,
				UsageEvents: usageEvents,
			},
			DataVersion: DataVersionCurrent,
		}},
		ExcludedSessionIDs: excluded,
		ResultSetComplete:  true,
	}, nil
}

type vibeSource struct {
	Root string
	Path string
}

type vibeSourceSet struct {
	roots []string
}

func newVibeSourceSet(roots []string) vibeSourceSet {
	return vibeSourceSet{roots: cleanJSONLRoots(roots)}
}

func (s vibeSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverVibeSessions(root) {
			source, ok := s.sourceRef(root, file.Path)
			if !ok {
				continue
			}
			addJSONLSource(source, &sources, seen)
		}
	}
	sortJSONLSources(sources)
	return sources, nil
}

func (s vibeSourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots))
	for _, root := range s.roots {
		roots = append(roots, WatchRoot{
			Path:         root,
			Recursive:    true,
			IncludeGlobs: []string{"messages.jsonl", "meta.json"},
			DebounceKey:  string(AgentVibe) + ":sessions:" + root,
		})
	}
	return WatchPlan{Roots: roots}, nil
}

func (s vibeSourceSet) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.WatchRoot != "" {
		root := filepath.Clean(req.WatchRoot)
		if !s.hasRoot(root) {
			return nil, nil
		}
		source, ok := s.sourceForEventPath(root, req.Path)
		if !ok {
			return nil, nil
		}
		return []SourceRef{source}, nil
	}
	for _, root := range s.roots {
		source, ok := s.sourceForEventPath(root, req.Path)
		if ok {
			return []SourceRef{source}, nil
		}
	}
	return nil, nil
}

func (s vibeSourceSet) FindSource(
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
		if source, ok := s.sourceForPath(path); ok {
			return source, true, nil
		}
	}
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	for _, root := range s.roots {
		path := FindVibeSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s vibeSourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	path, ok := s.pathFromSource(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("vibe source path unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return SourceFingerprint{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return SourceFingerprint{}, fmt.Errorf("stat %s: source is a directory", path)
	}
	size := info.Size()
	mtime := info.ModTime().UnixNano()
	metaPath := vibeMetaPath(path)
	if metaInfo, err := os.Stat(metaPath); err == nil {
		size += metaInfo.Size()
		if metaMTime := metaInfo.ModTime().UnixNano(); metaMTime > mtime {
			mtime = metaMTime
		}
	}
	hash, err := hashJSONLSourceFile(path)
	if err != nil {
		return SourceFingerprint{}, err
	}
	return SourceFingerprint{
		Key:     firstNonEmptyJSONLString(source.FingerprintKey, source.Key, path),
		Size:    size,
		MTimeNS: mtime,
		Hash:    hash,
	}, nil
}

func (s vibeSourceSet) pathFromSource(source SourceRef) (string, bool) {
	switch src := source.Opaque.(type) {
	case vibeSource:
		return src.Path, src.Path != ""
	case *vibeSource:
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
			src := ref.Opaque.(vibeSource)
			return src.Path, true
		}
	}
	return "", false
}

func (s vibeSourceSet) sourceForPath(path string) (SourceRef, bool) {
	for _, root := range s.roots {
		if source, ok := s.sourceForEventPath(root, path); ok {
			return source, true
		}
	}
	return SourceRef{}, false
}

func (s vibeSourceSet) sourceForEventPath(root, path string) (SourceRef, bool) {
	rel, ok := vibeRelPath(root, path)
	if !ok {
		return SourceRef{}, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || !isVibeSessionDirName(parts[0]) {
		return SourceRef{}, false
	}
	switch parts[1] {
	case "messages.jsonl":
		return s.sourceRef(root, filepath.Join(filepath.Clean(root), parts[0], "messages.jsonl"))
	case "meta.json":
		return s.sourceRef(root, filepath.Join(filepath.Clean(root), parts[0], "messages.jsonl"))
	default:
		return SourceRef{}, false
	}
}

func (s vibeSourceSet) sourceRef(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !isVibeMessagesFile(path) {
		return SourceRef{}, false
	}
	rel, ok := vibeRelPath(root, path)
	if !ok {
		return SourceRef{}, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || !isVibeSessionDirName(parts[0]) || parts[1] != "messages.jsonl" {
		return SourceRef{}, false
	}
	return SourceRef{
		Provider:       AgentVibe,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		ProjectHint:    parts[0],
		Opaque: vibeSource{
			Root: root,
			Path: path,
		},
	}, true
}

func (s vibeSourceSet) hasRoot(root string) bool {
	for _, configured := range s.roots {
		if samePath(root, configured) {
			return true
		}
	}
	return false
}

func vibeRelPath(root, path string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == "" {
		return "", false
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", false
	}
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return rel, true
}

func isVibeSessionDirName(name string) bool {
	return strings.HasPrefix(name, "session_") && strings.Contains(name, "_")
}

func vibeMetaPath(messagesPath string) string {
	return filepath.Join(filepath.Dir(messagesPath), "meta.json")
}

func vibeProviderExcludedSessionIDs(path, currentID string) []string {
	fallbackID := string(AgentVibe) + ":" + filepath.Base(filepath.Dir(path))
	if currentID == "" || currentID == fallbackID {
		return nil
	}
	return []string{fallbackID}
}

func vibeProviderCapabilities() Capabilities {
	return Capabilities{
		Source: SourceCapabilities{
			DiscoverSources:      CapabilitySupported,
			WatchSources:         CapabilitySupported,
			ClassifyChangedPath:  CapabilitySupported,
			FindSource:           CapabilitySupported,
			CompositeFingerprint: CapabilitySupported,
			MultiSessionSource:   CapabilityNotApplicable,
			PerSessionErrors:     CapabilityNotApplicable,
			ExcludedSessions:     CapabilitySupported,
			ForceReplaceOnParse:  CapabilityNotApplicable,
		},
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			SessionName:          CapabilitySupported,
			Cwd:                  CapabilitySupported,
			GitBranch:            CapabilitySupported,
			Relationships:        CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
