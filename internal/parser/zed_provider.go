package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var _ Provider = (*zedProvider)(nil)

type zedProviderFactory struct {
	def AgentDef
}

func newZedProviderFactory(def AgentDef) ProviderFactory {
	return zedProviderFactory{def: cloneAgentDef(def)}
}

func (f zedProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f zedProviderFactory) Capabilities() Capabilities {
	return zedProviderCapabilities()
}

func (f zedProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &zedProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   zedProviderCapabilities(),
			Config: cfg,
		},
		sources: newZedSourceSet(cfg.Roots),
	}
}

type zedProvider struct {
	ProviderBase
	sources zedSourceSet
}

func (p *zedProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *zedProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *zedProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *zedProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *zedProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *zedProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	src, ok := p.sources.sourceFromRef(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("zed source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	dbInfo, err := os.Stat(src.DBPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ParseOutcome{
				ResultSetComplete: true,
				ForceReplace:      true,
				SkipReason:        SkipNoSession,
			}, nil
		}
		return ParseOutcome{}, fmt.Errorf("stat %s: %w", src.DBPath, err)
	}

	if src.SessionID != "" {
		result, err := ParseZedThreadDirect(
			src.DBPath,
			src.SessionID,
			machine,
			dbInfo,
		)
		if err != nil {
			return ParseOutcome{}, err
		}
		if result == nil {
			return ParseOutcome{
				ResultSetComplete: true,
				ForceReplace:      true,
				SkipReason:        SkipNoSession,
			}, nil
		}
		if req.Fingerprint.Hash != "" {
			result.Session.File.Hash = req.Fingerprint.Hash
		}
		return ParseOutcome{
			Results: []ParseResultOutcome{{
				Result:      *result,
				DataVersion: DataVersionCurrent,
			}},
			ResultSetComplete: true,
			ForceReplace:      true,
		}, nil
	}

	conn, err := OpenZedDB(src.DBPath)
	if err != nil {
		return ParseOutcome{}, err
	}
	defer conn.Close()

	metas, err := ListZedThreadMetas(conn, src.DBPath)
	if err != nil {
		return ParseOutcome{}, err
	}
	dbHash, _ := hashJSONLSourceFile(src.DBPath)
	results := make([]ParseResultOutcome, 0, len(metas))
	for _, meta := range metas {
		result, err := ParseZedThreadFromDB(
			conn,
			src.DBPath,
			meta.RawID,
			machine,
			dbInfo,
		)
		if err != nil {
			return ParseOutcome{}, err
		}
		if result == nil {
			continue
		}
		if dbHash != "" {
			result.Session.File.Hash = dbHash
		}
		results = append(results, ParseResultOutcome{
			Result:      *result,
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

type zedSource struct {
	Root      string
	Path      string
	DBPath    string
	SessionID string
}

type zedSourceSet struct {
	roots []string
}

func newZedSourceSet(roots []string) zedSourceSet {
	return zedSourceSet{roots: cleanJSONLRoots(roots)}
}

func (s zedSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverZedSessions(root) {
			source, ok := s.sourceRef(root, file.Path)
			if ok {
				addJSONLSource(source, &sources, seen)
			}
		}
	}
	sortJSONLSources(sources)
	return sources, nil
}

func (s zedSourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots))
	for _, root := range s.roots {
		threadsDir := filepath.Join(root, "threads")
		roots = append(roots, WatchRoot{
			Path:         threadsDir,
			Recursive:    false,
			IncludeGlobs: []string{"threads.db", "threads.db-*"},
			DebounceKey:  string(AgentZed) + ":threads:" + threadsDir,
		})
	}
	return WatchPlan{Roots: roots}, nil
}

func (s zedSourceSet) SourcesForChangedPath(
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

func (s zedSourceSet) FindSource(
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
		path := FindZedSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s zedSourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	src, ok := s.sourceFromRef(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("zed source path unavailable")
	}
	info, err := os.Stat(src.DBPath)
	if err != nil {
		return SourceFingerprint{}, fmt.Errorf("stat %s: %w", src.DBPath, err)
	}
	mtime := info.ModTime().UnixNano()
	if src.SessionID != "" {
		if sessionMtime, err := ZedSQLiteSourceMtime(src.Path); err == nil {
			mtime = sessionMtime
		}
	} else if compositeMtime, err := sqliteDBCompositeMtime(src.DBPath); err == nil {
		mtime = compositeMtime
	}
	// Zed has no cheap per-thread content digest; legacy sync stored the
	// physical DB hash on virtual thread rows while per-thread updated_at
	// remained the mtime freshness signal.
	hash, err := hashJSONLSourceFile(src.DBPath)
	if err != nil {
		return SourceFingerprint{}, err
	}
	return SourceFingerprint{
		Key:     firstNonEmptyJSONLString(source.FingerprintKey, source.Key, src.Path),
		Size:    info.Size(),
		MTimeNS: mtime,
		Hash:    hash,
	}, nil
}

func (s zedSourceSet) sourceFromRef(source SourceRef) (zedSource, bool) {
	switch src := source.Opaque.(type) {
	case zedSource:
		return src, src.DBPath != "" && src.Path != ""
	case *zedSource:
		if src != nil && src.DBPath != "" && src.Path != "" {
			return *src, true
		}
	}
	for _, candidate := range []string{source.DisplayPath, source.FingerprintKey, source.Key} {
		for _, root := range s.roots {
			if ref, ok := s.sourceRef(root, candidate); ok {
				src := ref.Opaque.(zedSource)
				return src, true
			}
		}
	}
	return zedSource{}, false
}

func (s zedSourceSet) sourceRef(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if dbPath, sessionID, ok := ParseZedSQLiteVirtualPath(path); ok {
		if !zedDBUnderRoot(root, dbPath, true) {
			return SourceRef{}, false
		}
		return s.newSourceRef(root, path, dbPath, sessionID), true
	}
	if !zedDBUnderRoot(root, path, true) {
		return SourceRef{}, false
	}
	return s.newSourceRef(root, path, path, ""), true
}

func (s zedSourceSet) sourceRefForChangedPath(root, path string) (SourceRef, bool) {
	if source, ok := s.sourceRef(root, path); ok {
		return source, true
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if dbPath, sessionID, ok := ParseZedSQLiteVirtualPath(path); ok {
		if !zedDBUnderRoot(root, dbPath, false) {
			return SourceRef{}, false
		}
		return s.newSourceRef(root, path, dbPath, sessionID), true
	}
	dbPath, ok := zedDBPathForEvent(root, path)
	if !ok {
		return SourceRef{}, false
	}
	return s.newSourceRef(root, dbPath, dbPath, ""), true
}

func zedDBUnderRoot(root, dbPath string, requireRegular bool) bool {
	root = filepath.Clean(root)
	dbPath = filepath.Clean(dbPath)
	rel, ok := relUnder(root, dbPath)
	if !ok || filepath.ToSlash(rel) != "threads/threads.db" {
		return false
	}
	return !requireRegular || IsRegularFile(dbPath)
}

func zedDBPathForEvent(root, path string) (string, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, ok := relUnder(root, path)
	if !ok {
		return "", false
	}
	relSlash := filepath.ToSlash(rel)
	if relSlash == "threads/threads.db" ||
		(filepath.ToSlash(filepath.Dir(rel)) == "threads" &&
			strings.HasPrefix(filepath.Base(rel), "threads.db-")) {
		return filepath.Join(root, zedThreadsDBRelPath), true
	}
	return "", false
}

func (s zedSourceSet) newSourceRef(
	root, path, dbPath, sessionID string,
) SourceRef {
	return SourceRef{
		Provider:       AgentZed,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		Opaque: zedSource{
			Root:      root,
			Path:      path,
			DBPath:    dbPath,
			SessionID: sessionID,
		},
	}
}

func sqliteDBCompositeMtime(dbPath string) (int64, error) {
	var maxMtime int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(dbPath + suffix)
		if err != nil {
			continue
		}
		if mtime := info.ModTime().UnixNano(); mtime > maxMtime {
			maxMtime = mtime
		}
	}
	if maxMtime == 0 {
		return 0, &os.PathError{Op: "stat", Path: dbPath, Err: os.ErrNotExist}
	}
	return maxMtime, nil
}

func zedProviderCapabilities() Capabilities {
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
			SessionName:          CapabilitySupported,
			Cwd:                  CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
