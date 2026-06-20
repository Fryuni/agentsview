package parser

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

var _ Provider = (*antigravityCLIProvider)(nil)

type antigravityCLIProviderFactory struct {
	def AgentDef
}

func newAntigravityCLIProviderFactory(def AgentDef) ProviderFactory {
	return antigravityCLIProviderFactory{def: cloneAgentDef(def)}
}

func (f antigravityCLIProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f antigravityCLIProviderFactory) Capabilities() Capabilities {
	return antigravityCLIProviderCapabilities()
}

func (f antigravityCLIProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &antigravityCLIProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   antigravityCLIProviderCapabilities(),
			Config: cfg,
		},
		sources: newAntigravityCLISourceSet(cfg.Roots),
	}
}

type antigravityCLIProvider struct {
	ProviderBase
	sources antigravityCLISourceSet
}

func (p *antigravityCLIProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *antigravityCLIProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *antigravityCLIProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *antigravityCLIProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *antigravityCLIProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *antigravityCLIProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	src, ok := p.sources.sourceFromRef(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("antigravity cli source path unavailable")
	}
	if _, err := os.Stat(src.Path); err != nil {
		if os.IsNotExist(err) {
			return ParseOutcome{
				ResultSetComplete: true,
				ForceReplace:      true,
				SkipReason:        SkipNoSession,
			}, nil
		}
		return ParseOutcome{}, fmt.Errorf("stat %s: %w", src.Path, err)
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, usageEvents, status, err := ParseAntigravityCLISessionWithStatus(
		src.Path,
		req.Source.ProjectHint,
		machine,
	)
	if err != nil {
		return ParseOutcome{}, err
	}
	if sess == nil {
		return ParseOutcome{
			ResultSetComplete: true,
			ForceReplace:      true,
			SkipReason:        SkipNoSession,
		}, nil
	}
	if req.Fingerprint.Hash != "" {
		sess.File.Hash = req.Fingerprint.Hash
	}
	result := ParseResultOutcome{
		Result: ParseResult{
			Session:     *sess,
			Messages:    msgs,
			UsageEvents: usageEvents,
		},
		DataVersion: DataVersionCurrent,
	}
	if status.NeedsRetry {
		result.DataVersion = DataVersionNeedsRetry
		result.RetryReason = "antigravity cli source needs high-fidelity retry"
	}
	return ParseOutcome{
		Results:           []ParseResultOutcome{result},
		ResultSetComplete: true,
		ForceReplace:      true,
	}, nil
}

type antigravityCLISource struct {
	Root    string
	Path    string
	ID      string
	Project string
}

type antigravityCLISourceSet struct {
	roots   []string
	history *antigravityCLIHistoryState
}

func newAntigravityCLISourceSet(roots []string) antigravityCLISourceSet {
	roots = cleanJSONLRoots(roots)
	return antigravityCLISourceSet{
		roots:   roots,
		history: newAntigravityCLIHistoryState(roots),
	}
}

func (s antigravityCLISourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverAntigravityCLISessions(root) {
			source, ok := s.sourceRef(root, file.Path, file.Project, false)
			if ok {
				addJSONLSource(source, &sources, seen)
			}
		}
	}
	sortJSONLSources(sources)
	return sources, nil
}

func (s antigravityCLISourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots)*4)
	for _, root := range s.roots {
		roots = append(roots,
			WatchRoot{
				Path:         filepath.Join(root, "brain"),
				Recursive:    true,
				IncludeGlobs: []string{"*.md", "*.md.metadata.json"},
				DebounceKey:  string(AgentAntigravityCLI) + ":brain:" + root,
			},
			WatchRoot{
				Path:         filepath.Join(root, "conversations"),
				Recursive:    false,
				IncludeGlobs: []string{"*.db", "*.db-*", "*.pb", "*.trajectory.json"},
				DebounceKey:  string(AgentAntigravityCLI) + ":conversations:" + root,
			},
			WatchRoot{
				Path:         root,
				Recursive:    false,
				IncludeGlobs: []string{"history.jsonl"},
				DebounceKey:  string(AgentAntigravityCLI) + ":history:" + root,
			},
			WatchRoot{
				Path:         filepath.Join(root, "implicit"),
				Recursive:    false,
				IncludeGlobs: []string{"*.pb", "*.trajectory.json"},
				DebounceKey:  string(AgentAntigravityCLI) + ":implicit:" + root,
			},
		)
	}
	return WatchPlan{Roots: roots}, nil
}

func (s antigravityCLISourceSet) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, root := range s.roots {
		if req.WatchRoot != "" && !antigravityCLIWatchRootMatches(root, req.WatchRoot) {
			continue
		}
		if sources := s.sourcesForChangedPath(root, req); len(sources) > 0 {
			return sources, nil
		}
	}
	return nil, nil
}

func (s antigravityCLISourceSet) FindSource(
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
			if source, ok := s.storedSourceRef(root, path); ok {
				return source, true, nil
			}
		}
	}
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	projects := make(map[string]map[string]string)
	for _, root := range s.roots {
		path := FindAntigravityCLISourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		project := ""
		id := strings.TrimPrefix(req.RawSessionID, antigravityImplicitTag)
		if projects[root] == nil {
			projects[root] = buildAntigravityProjectMap(
				filepath.Join(root, "history.jsonl"),
			)
		}
		project = projects[root][id]
		if source, ok := s.sourceRef(root, path, project, false); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s antigravityCLISourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	src, ok := s.sourceFromRef(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("antigravity cli source path unavailable")
	}
	key := firstNonEmptyJSONLString(source.FingerprintKey, source.Key, src.Path)
	info, err := AntigravityCLIFileInfo(src.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return SourceFingerprint{Key: key}, nil
		}
		return SourceFingerprint{}, err
	}
	hash, err := antigravityCLICompositeHash(src.Path, src.ID)
	if err != nil {
		return SourceFingerprint{}, err
	}
	return SourceFingerprint{
		Key:     key,
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
		Hash:    hash,
	}, nil
}

func (s antigravityCLISourceSet) sourceFromRef(
	source SourceRef,
) (antigravityCLISource, bool) {
	switch src := source.Opaque.(type) {
	case antigravityCLISource:
		return src, src.Path != ""
	case *antigravityCLISource:
		if src != nil && src.Path != "" {
			return *src, true
		}
	}
	for _, candidate := range []string{source.DisplayPath, source.FingerprintKey, source.Key} {
		for _, root := range s.roots {
			if ref, ok := s.sourceRef(root, candidate, source.ProjectHint, true); ok {
				src := ref.Opaque.(antigravityCLISource)
				return src, true
			}
		}
	}
	return antigravityCLISource{}, false
}

func (s antigravityCLISourceSet) sourcesForChangedPath(
	root string,
	req ChangedPathRequest,
) []SourceRef {
	root = filepath.Clean(root)
	path := filepath.Clean(req.Path)
	if samePath(path, filepath.Join(root, "history.jsonl")) {
		return s.sourcesForHistoryChange(root, req)
	}
	if sourcePath, id, ok := antigravityCLISourcePathForEvent(root, path); ok {
		if source, ok := s.sourceRef(root, sourcePath, s.projectForID(root, id), true); ok {
			return []SourceRef{source}
		}
	}
	if id, ok := antigravityBrainID(root, path); ok {
		var sources []SourceRef
		for _, sourcePath := range []string{
			antigravityCLIConversationSource(root, id),
			filepath.Join(root, "implicit", id+".pb"),
		} {
			if sourcePath == "" || !IsRegularFile(sourcePath) {
				continue
			}
			source, ok := s.sourceRef(root, sourcePath, s.projectForID(root, id), false)
			if ok {
				sources = append(sources, source)
			}
		}
		sortJSONLSources(sources)
		return sources
	}
	return nil
}

func (s antigravityCLISourceSet) sourcesForHistoryChange(
	root string,
	req ChangedPathRequest,
) []SourceRef {
	if antigravityCLIHistoryChangeIsDestructive(req) {
		s.historyState().clear(root)
		return s.sourcesForUntaggedHistoryChange(root)
	}

	snapshot, err := readAntigravityCLIHistorySnapshot(
		filepath.Join(root, "history.jsonl"),
	)
	if err != nil {
		s.historyState().clear(root)
		return s.sourcesForUntaggedHistoryChange(root)
	}

	state := s.historyState()
	previous, hadPrevious := state.snapshot(root)
	state.update(root, snapshot)
	if snapshot.hasUntaggedRows ||
		!snapshot.hasRows ||
		!hadPrevious ||
		previous.hasUntaggedRows ||
		!antigravityCLIHistoryIDsSuperset(snapshot.ids, previous.ids) {
		return s.sourcesForUntaggedHistoryChange(root)
	}
	return s.sourcesForHistoryIDs(root, snapshot.ids)
}

func (s antigravityCLISourceSet) sourcesForHistoryIDs(
	root string,
	ids map[string]struct{},
) []SourceRef {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for id := range ids {
		if !IsValidSessionID(id) {
			continue
		}
		project := s.projectForID(root, id)
		if sourcePath := antigravityCLIConversationSource(root, id); sourcePath != "" {
			source, ok := s.sourceRef(root, sourcePath, project, false)
			if ok {
				addJSONLSource(source, &sources, seen)
			}
		}
		implicitPath := filepath.Join(root, "implicit", id+".pb")
		if IsRegularFile(implicitPath) {
			source, ok := s.sourceRef(root, implicitPath, project, false)
			if ok {
				addJSONLSource(source, &sources, seen)
			}
		}
	}
	sortJSONLSources(sources)
	return sources
}

func (s antigravityCLISourceSet) historyState() *antigravityCLIHistoryState {
	if s.history != nil {
		return s.history
	}
	return newAntigravityCLIHistoryState(nil)
}

func (s antigravityCLISourceSet) sourcesForUntaggedHistoryChange(root string) []SourceRef {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, file := range DiscoverAntigravityCLISessions(root) {
		source, ok := s.sourceRef(root, file.Path, file.Project, false)
		if ok {
			addJSONLSource(source, &sources, seen)
		}
	}
	sortJSONLSources(sources)
	return sources
}

type antigravityCLIHistorySnapshot struct {
	ids             map[string]struct{}
	hasRows         bool
	hasUntaggedRows bool
}

type antigravityCLIHistoryState struct {
	mu        sync.Mutex
	snapshots map[string]antigravityCLIHistorySnapshot
}

func newAntigravityCLIHistoryState(roots []string) *antigravityCLIHistoryState {
	state := &antigravityCLIHistoryState{
		snapshots: make(map[string]antigravityCLIHistorySnapshot),
	}
	for _, root := range roots {
		snapshot, err := readAntigravityCLIHistorySnapshot(
			filepath.Join(root, "history.jsonl"),
		)
		if err == nil {
			state.update(root, snapshot)
		}
	}
	return state
}

func (s *antigravityCLIHistoryState) snapshot(
	root string,
) (antigravityCLIHistorySnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[filepath.Clean(root)]
	if !ok {
		return antigravityCLIHistorySnapshot{}, false
	}
	return cloneAntigravityCLIHistorySnapshot(snapshot), true
}

func (s *antigravityCLIHistoryState) update(
	root string,
	snapshot antigravityCLIHistorySnapshot,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[filepath.Clean(root)] = cloneAntigravityCLIHistorySnapshot(snapshot)
}

func (s *antigravityCLIHistoryState) clear(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.snapshots, filepath.Clean(root))
}

func readAntigravityCLIHistorySnapshot(
	historyPath string,
) (antigravityCLIHistorySnapshot, error) {
	snapshot := antigravityCLIHistorySnapshot{
		ids: make(map[string]struct{}),
	}
	f, err := os.Open(historyPath)
	if err != nil {
		return snapshot, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		snapshot.hasRows = true
		cid := gjson.GetBytes(line, "conversationId").Str
		if cid == "" {
			snapshot.hasUntaggedRows = true
			continue
		}
		if IsValidSessionID(cid) {
			snapshot.ids[cid] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func cloneAntigravityCLIHistorySnapshot(
	snapshot antigravityCLIHistorySnapshot,
) antigravityCLIHistorySnapshot {
	return antigravityCLIHistorySnapshot{
		ids:             cloneAntigravityCLIHistoryIDs(snapshot.ids),
		hasRows:         snapshot.hasRows,
		hasUntaggedRows: snapshot.hasUntaggedRows,
	}
}

func cloneAntigravityCLIHistoryIDs(ids map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func antigravityCLIHistoryIDsSuperset(
	current map[string]struct{},
	previous map[string]struct{},
) bool {
	for id := range previous {
		if _, ok := current[id]; !ok {
			return false
		}
	}
	return true
}

func antigravityCLIHistoryChangeIsDestructive(req ChangedPathRequest) bool {
	switch strings.ToLower(req.EventKind) {
	case "remove", "removed", "delete", "deleted", "rename", "renamed":
		return true
	default:
		return false
	}
}

func (s antigravityCLISourceSet) storedSourceRef(
	root, path string,
) (SourceRef, bool) {
	id, ok := antigravityCLISessionIDForPath(root, path)
	if !ok {
		return SourceRef{}, false
	}
	projectID := strings.TrimPrefix(id, antigravityImplicitTag)
	if currentPath := FindAntigravityCLISourceFile(root, id); currentPath != "" {
		return s.sourceRef(root, currentPath, s.projectForID(root, projectID), false)
	}
	return s.sourceRef(root, path, s.projectForID(root, projectID), true)
}

func (s antigravityCLISourceSet) sourceRef(
	root, path, project string,
	allowMissing bool,
) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	id, ok := antigravityCLISessionIDForPath(root, path)
	if !ok {
		return SourceRef{}, false
	}
	if !allowMissing && !IsRegularFile(path) {
		return SourceRef{}, false
	}
	if project == "" {
		project = s.projectForID(root, strings.TrimPrefix(id, antigravityImplicitTag))
	}
	return s.newSourceRef(root, path, id, project), true
}

func (s antigravityCLISourceSet) newSourceRef(
	root, path, id, project string,
) SourceRef {
	return SourceRef{
		Provider:       AgentAntigravityCLI,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		ProjectHint:    project,
		Opaque: antigravityCLISource{
			Root:    root,
			Path:    path,
			ID:      id,
			Project: project,
		},
	}
}

func (s antigravityCLISourceSet) projectForID(root, id string) string {
	return buildAntigravityProjectMap(filepath.Join(root, "history.jsonl"))[id]
}

func antigravityCLISourcePathForEvent(root, path string) (string, string, bool) {
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || (parts[0] != "conversations" && parts[0] != "implicit") {
		return "", "", false
	}
	name := parts[1]
	switch {
	case strings.HasSuffix(name, ".db") ||
		strings.HasSuffix(name, ".db-wal") ||
		strings.HasSuffix(name, ".db-shm"):
		if parts[0] != "conversations" {
			return "", "", false
		}
		base := strings.TrimSuffix(strings.TrimSuffix(name, "-wal"), "-shm")
		id := strings.TrimSuffix(base, ".db")
		if !IsValidSessionID(id) {
			return "", "", false
		}
		return filepath.Join(root, "conversations", id+".db"), id, true
	case strings.HasSuffix(name, ".pb"):
		id := strings.TrimSuffix(name, ".pb")
		if !IsValidSessionID(id) {
			return "", "", false
		}
		if parts[0] == "conversations" {
			if dbPath := antigravityCLIConversationSource(root, id); dbPath != "" {
				return dbPath, id, true
			}
		}
		return filepath.Join(root, parts[0], id+".pb"), id, true
	case strings.HasSuffix(name, ".trajectory.json"):
		id := strings.TrimSuffix(name, ".trajectory.json")
		if !IsValidSessionID(id) {
			return "", "", false
		}
		if parts[0] == "conversations" {
			sourcePath := antigravityCLIConversationSource(root, id)
			if sourcePath == "" {
				return "", "", false
			}
			return sourcePath, id, true
		}
		sourcePath := filepath.Join(root, parts[0], id+".pb")
		if !IsRegularFile(sourcePath) {
			return "", "", false
		}
		return sourcePath, id, true
	default:
		return "", "", false
	}
}

func antigravityCLIConversationSource(root, id string) string {
	for _, path := range []string{
		filepath.Join(root, "conversations", id+".db"),
		filepath.Join(root, "conversations", id+".pb"),
	} {
		if IsRegularFile(path) {
			return path
		}
	}
	return ""
}

func antigravityCLISessionIDForPath(root, path string) (string, bool) {
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || (parts[0] != "conversations" && parts[0] != "implicit") {
		return "", false
	}
	id, ext, ok := antigravityCLIPathID(parts[1])
	if !ok {
		return "", false
	}
	if parts[0] == "implicit" {
		if ext != ".pb" {
			return "", false
		}
		return antigravityImplicitTag + id, true
	}
	return id, true
}

func antigravityCLIWatchRootMatches(root, watchRoot string) bool {
	watchRoot = filepath.Clean(watchRoot)
	for _, subdir := range []string{"brain", "conversations", "implicit"} {
		if samePath(watchRoot, filepath.Join(root, subdir)) {
			return true
		}
	}
	return samePath(watchRoot, filepath.Clean(root))
}

func antigravityCLIProviderCapabilities() Capabilities {
	source := jsonlFileProviderSourceCapabilities()
	source.ForceReplaceOnParse = CapabilitySupported
	return Capabilities{
		Source: source,
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
