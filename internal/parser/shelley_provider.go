package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var _ Provider = (*shelleyProvider)(nil)

type shelleyProviderFactory struct {
	def AgentDef
}

func newShelleyProviderFactory(def AgentDef) ProviderFactory {
	return shelleyProviderFactory{def: cloneAgentDef(def)}
}

func (f shelleyProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f shelleyProviderFactory) Capabilities() Capabilities {
	return shelleyProviderCapabilities()
}

func (f shelleyProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &shelleyProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   shelleyProviderCapabilities(),
			Config: cfg,
		},
		sources: newShelleySourceSet(cfg.Roots),
	}
}

type shelleyProvider struct {
	ProviderBase
	sources shelleySourceSet
}

func (p *shelleyProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *shelleyProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *shelleyProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *shelleyProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *shelleyProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *shelleyProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	src, ok := p.sources.sourceFromRef(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("shelley source path unavailable")
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

	if src.ConversationID != "" {
		result, err := ParseShelleyConversationDirect(
			src.DBPath,
			src.ConversationID,
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

	conn, err := OpenShelleyDB(src.DBPath)
	if err != nil {
		return ParseOutcome{}, err
	}
	defer conn.Close()

	metas, err := ListShelleyConversationMetas(conn, src.DBPath)
	if err != nil {
		return ParseOutcome{}, err
	}
	results := make([]ParseResultOutcome, 0, len(metas))
	for _, meta := range metas {
		result, err := ParseShelleyConversationFromDB(
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

type shelleySource struct {
	Root           string
	Path           string
	DBPath         string
	ConversationID string
}

type shelleySourceSet struct {
	roots []string
}

func newShelleySourceSet(roots []string) shelleySourceSet {
	return shelleySourceSet{roots: cleanJSONLRoots(roots)}
}

func (s shelleySourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverShelleySessions(root) {
			source, ok := s.sourceRef(root, file.Path)
			if ok {
				addJSONLSource(source, &sources, seen)
			}
		}
	}
	sortJSONLSources(sources)
	return sources, nil
}

func (s shelleySourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots))
	for _, root := range s.roots {
		roots = append(roots, WatchRoot{
			Path:         root,
			Recursive:    false,
			IncludeGlobs: []string{shelleyDBName, shelleyDBName + "-*"},
			DebounceKey:  string(AgentShelley) + ":db:" + root,
		})
	}
	return WatchPlan{Roots: roots}, nil
}

func (s shelleySourceSet) SourcesForChangedPath(
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

func (s shelleySourceSet) FindSource(
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
		path := FindShelleySourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s shelleySourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	src, ok := s.sourceFromRef(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("shelley source path unavailable")
	}
	info, err := os.Stat(src.DBPath)
	if err != nil {
		return SourceFingerprint{}, fmt.Errorf("stat %s: %w", src.DBPath, err)
	}
	fingerprint := SourceFingerprint{
		Key:     firstNonEmptyJSONLString(source.FingerprintKey, source.Key, src.Path),
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
	}
	if src.ConversationID == "" {
		if compositeMtime, err := sqliteDBCompositeMtime(src.DBPath); err == nil {
			fingerprint.MTimeNS = compositeMtime
		}
		fingerprint.Hash, err = hashJSONLSourceFile(src.DBPath)
		if err != nil {
			return SourceFingerprint{}, err
		}
		return fingerprint, nil
	}

	conn, err := OpenShelleyDB(src.DBPath)
	if err != nil {
		return SourceFingerprint{}, err
	}
	defer conn.Close()
	metas, err := ListShelleyConversationMetas(conn, src.DBPath)
	if err != nil {
		return SourceFingerprint{}, err
	}
	for _, meta := range metas {
		if meta.RawID != src.ConversationID {
			continue
		}
		fingerprint.MTimeNS = meta.FileMtime
		fingerprint.Hash = meta.Fingerprint
		return fingerprint, nil
	}
	return SourceFingerprint{}, fmt.Errorf("shelley conversation not found: %s", src.ConversationID)
}

func (s shelleySourceSet) sourceFromRef(source SourceRef) (shelleySource, bool) {
	switch src := source.Opaque.(type) {
	case shelleySource:
		return src, src.DBPath != "" && src.Path != ""
	case *shelleySource:
		if src != nil && src.DBPath != "" && src.Path != "" {
			return *src, true
		}
	}
	for _, candidate := range []string{source.DisplayPath, source.FingerprintKey, source.Key} {
		for _, root := range s.roots {
			if ref, ok := s.sourceRef(root, candidate); ok {
				src := ref.Opaque.(shelleySource)
				return src, true
			}
		}
	}
	return shelleySource{}, false
}

func (s shelleySourceSet) sourceRef(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if dbPath, conversationID, ok := ParseShelleyVirtualPath(path); ok {
		if !shelleyDBUnderRoot(root, dbPath, true) {
			return SourceRef{}, false
		}
		return s.newSourceRef(root, path, dbPath, conversationID), true
	}
	if !shelleyDBUnderRoot(root, path, true) {
		return SourceRef{}, false
	}
	return s.newSourceRef(root, path, path, ""), true
}

func (s shelleySourceSet) sourceRefForChangedPath(
	root, path string,
) (SourceRef, bool) {
	if source, ok := s.sourceRef(root, path); ok {
		return source, true
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if dbPath, conversationID, ok := ParseShelleyVirtualPath(path); ok {
		if !shelleyDBUnderRoot(root, dbPath, false) {
			return SourceRef{}, false
		}
		return s.newSourceRef(root, path, dbPath, conversationID), true
	}
	dbPath, ok := shelleyDBPathForEvent(root, path)
	if !ok {
		return SourceRef{}, false
	}
	return s.newSourceRef(root, dbPath, dbPath, ""), true
}

func shelleyDBUnderRoot(root, dbPath string, requireRegular bool) bool {
	root = filepath.Clean(root)
	dbPath = filepath.Clean(dbPath)
	rel, ok := relUnder(root, dbPath)
	if !ok || filepath.ToSlash(rel) != shelleyDBName {
		return false
	}
	return !requireRegular || IsRegularFile(dbPath)
}

func shelleyDBPathForEvent(root, path string) (string, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, ok := relUnder(root, path)
	if !ok {
		return "", false
	}
	if filepath.ToSlash(rel) == shelleyDBName ||
		(filepath.Dir(rel) == "." &&
			strings.HasPrefix(filepath.Base(rel), shelleyDBName+"-")) {
		return filepath.Join(root, shelleyDBName), true
	}
	return "", false
}

func (s shelleySourceSet) newSourceRef(
	root, path, dbPath, conversationID string,
) SourceRef {
	return SourceRef{
		Provider:       AgentShelley,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		Opaque: shelleySource{
			Root:           root,
			Path:           path,
			DBPath:         dbPath,
			ConversationID: conversationID,
		},
	}
}

func shelleyProviderCapabilities() Capabilities {
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
			Relationships:        CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
