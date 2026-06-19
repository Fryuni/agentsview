package parser

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var _ Provider = (*hermesProvider)(nil)

type hermesProviderFactory struct {
	def AgentDef
}

func newHermesProviderFactory(def AgentDef) ProviderFactory {
	return hermesProviderFactory{def: cloneAgentDef(def)}
}

func (f hermesProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f hermesProviderFactory) Capabilities() Capabilities {
	return hermesProviderCapabilities()
}

func (f hermesProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &hermesProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   hermesProviderCapabilities(),
			Config: cfg,
		},
		sources: newHermesSourceSet(cfg.Roots),
	}
}

type hermesProvider struct {
	ProviderBase
	sources hermesSourceSet
}

func (p *hermesProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *hermesProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *hermesProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *hermesProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *hermesProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *hermesProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("hermes source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	if filepath.Base(path) == "state.db" {
		results, err := ParseHermesArchive(path, req.Source.ProjectHint, machine)
		if err != nil {
			return ParseOutcome{}, err
		}
		out := make([]ParseResultOutcome, 0, len(results))
		for _, result := range results {
			out = append(out, ParseResultOutcome{
				Result:      result,
				DataVersion: DataVersionCurrent,
			})
		}
		return ParseOutcome{
			Results:           out,
			ResultSetComplete: true,
			ForceReplace:      true,
		}, nil
	}

	sess, msgs, err := ParseHermesSession(path, req.Source.ProjectHint, machine)
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

type hermesSource struct {
	Root string
	Path string
}

type hermesSourceSet struct {
	roots []string
}

func newHermesSourceSet(roots []string) hermesSourceSet {
	return hermesSourceSet{roots: cleanJSONLRoots(roots)}
}

func (s hermesSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverHermesSessions(root) {
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

func (s hermesSourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots))
	for _, root := range s.roots {
		roots = append(roots, WatchRoot{
			Path:         root,
			Recursive:    true,
			IncludeGlobs: []string{"state.db", "*.jsonl", "session_*.json"},
			DebounceKey:  string(AgentHermes) + ":sessions:" + root,
		})
	}
	return WatchPlan{Roots: roots}, nil
}

func (s hermesSourceSet) SourcesForChangedPath(
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
		source, ok := s.sourceForPath(root, req.Path)
		if !ok {
			return nil, nil
		}
		return []SourceRef{source}, nil
	}
	for _, root := range s.roots {
		source, ok := s.sourceForPath(root, req.Path)
		if ok {
			return []SourceRef{source}, nil
		}
	}
	return nil, nil
}

func (s hermesSourceSet) FindSource(
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
			if source, ok := s.sourceForPath(root, path); ok {
				return source, true, nil
			}
		}
	}
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	for _, root := range s.roots {
		if stateDB, _, ok := hermesStatePaths(root); ok &&
			IsValidSessionID(req.RawSessionID) {
			if source, ok := s.sourceRef(root, stateDB); ok {
				return source, true, nil
			}
		}
		transcriptRoot := hermesTranscriptRoot(root)
		path := FindHermesSourceFile(transcriptRoot, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s hermesSourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	path, ok := s.pathFromSource(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("hermes source path unavailable")
	}
	if filepath.Base(path) == "state.db" {
		return hermesArchiveFingerprint(source, path)
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

func (s hermesSourceSet) pathFromSource(source SourceRef) (string, bool) {
	switch src := source.Opaque.(type) {
	case hermesSource:
		return src.Path, src.Path != ""
	case *hermesSource:
		if src != nil && src.Path != "" {
			return src.Path, true
		}
	}
	for _, candidate := range []string{
		source.DisplayPath,
		source.FingerprintKey,
		source.Key,
	} {
		for _, root := range s.roots {
			if ref, ok := s.sourceForPath(root, candidate); ok {
				src := ref.Opaque.(hermesSource)
				return src.Path, true
			}
		}
	}
	return "", false
}

func (s hermesSourceSet) sourceForPath(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if stateDB, sessionsDir, ok := hermesStatePaths(root); ok {
		if samePath(path, stateDB) || hermesPathInTranscriptDir(sessionsDir, path) {
			return s.sourceRef(root, stateDB)
		}
		return SourceRef{}, false
	}
	return s.sourceRef(root, path)
}

func (s hermesSourceSet) sourceRef(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if stateDB, _, ok := hermesStatePaths(root); ok && samePath(path, stateDB) {
		return SourceRef{
			Provider:       AgentHermes,
			Key:            stateDB,
			DisplayPath:    stateDB,
			FingerprintKey: stateDB,
			Opaque: hermesSource{
				Root: root,
				Path: stateDB,
			},
		}, true
	}
	transcriptRoot := hermesTranscriptRoot(root)
	if !hermesPathInTranscriptDir(transcriptRoot, path) || !IsRegularFile(path) {
		return SourceRef{}, false
	}
	return SourceRef{
		Provider:       AgentHermes,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		Opaque: hermesSource{
			Root: root,
			Path: path,
		},
	}, true
}

func (s hermesSourceSet) hasRoot(root string) bool {
	for _, configured := range s.roots {
		if samePath(root, configured) {
			return true
		}
	}
	return false
}

func hermesTranscriptRoot(root string) string {
	root = filepath.Clean(root)
	if _, sessionsDir, ok := hermesStatePaths(root); ok {
		return sessionsDir
	}
	childSessions := filepath.Join(root, "sessions")
	if info, err := os.Stat(childSessions); err == nil && info.IsDir() {
		return childSessions
	}
	return root
}

func hermesPathInTranscriptDir(dir, path string) bool {
	dir = filepath.Clean(dir)
	path = filepath.Clean(path)
	if !samePath(filepath.Dir(path), dir) {
		return false
	}
	name := filepath.Base(path)
	if strings.HasSuffix(name, ".jsonl") {
		return true
	}
	return strings.HasSuffix(name, ".json") && strings.HasPrefix(name, "session_")
}

func hermesArchiveFingerprint(source SourceRef, stateDB string) (SourceFingerprint, error) {
	stateInfo, err := os.Stat(stateDB)
	if err != nil {
		return SourceFingerprint{}, fmt.Errorf("stat %s: %w", stateDB, err)
	}
	if stateInfo.IsDir() {
		return SourceFingerprint{}, fmt.Errorf("stat %s: source is a directory", stateDB)
	}
	fingerprint := SourceFingerprint{
		Key: firstNonEmptyJSONLString(
			source.FingerprintKey,
			source.Key,
			stateDB,
		),
		Size:    stateInfo.Size(),
		MTimeNS: stateInfo.ModTime().UnixNano(),
	}
	h := sha256.New()
	if err := addHermesFingerprintPart(h, "state", stateDB, stateInfo); err != nil {
		return SourceFingerprint{}, err
	}
	_, sessionsDir, _ := hermesStatePaths(stateDB)
	for _, file := range discoverHermesTranscriptFiles(sessionsDir) {
		info, err := os.Stat(file.Path)
		if err != nil {
			return SourceFingerprint{}, fmt.Errorf("stat %s: %w", file.Path, err)
		}
		fingerprint.Size += info.Size()
		if mtime := info.ModTime().UnixNano(); mtime > fingerprint.MTimeNS {
			fingerprint.MTimeNS = mtime
		}
		if err := addHermesFingerprintPart(h, "transcript", file.Path, info); err != nil {
			return SourceFingerprint{}, err
		}
	}
	fingerprint.Hash = fmt.Sprintf("%x", h.Sum(nil))
	return fingerprint, nil
}

func addHermesFingerprintPart(
	h hash.Hash,
	label string,
	path string,
	info os.FileInfo,
) error {
	if _, err := fmt.Fprintf(
		h,
		"%s\x00%s\x00%d\x00%d\x00",
		label,
		path,
		info.Size(),
		info.ModTime().UnixNano(),
	); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}
	return nil
}

func hermesProviderCapabilities() Capabilities {
	return Capabilities{
		Source: SourceCapabilities{
			DiscoverSources:      CapabilitySupported,
			WatchSources:         CapabilitySupported,
			ClassifyChangedPath:  CapabilitySupported,
			FindSource:           CapabilitySupported,
			CompositeFingerprint: CapabilitySupported,
			MultiSessionSource:   CapabilitySupported,
			PerSessionErrors:     CapabilityNotApplicable,
			ExcludedSessions:     CapabilityNotApplicable,
			ForceReplaceOnParse:  CapabilitySupported,
		},
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			SessionName:          CapabilitySupported,
			Relationships:        CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
