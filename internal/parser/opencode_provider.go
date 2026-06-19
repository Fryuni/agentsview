package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var _ Provider = (*openCodeFormatProvider)(nil)

type openCodeFormatProviderFactory struct {
	def  AgentDef
	spec openCodeProviderSpec
}

func newOpenCodeProviderFactory(def AgentDef) ProviderFactory {
	return openCodeFormatProviderFactory{
		def:  cloneAgentDef(def),
		spec: openCodeProviderSpecForAgent(AgentOpenCode),
	}
}

func newKiloProviderFactory(def AgentDef) ProviderFactory {
	return openCodeFormatProviderFactory{
		def:  cloneAgentDef(def),
		spec: openCodeProviderSpecForAgent(AgentKilo),
	}
}

func newMiMoCodeProviderFactory(def AgentDef) ProviderFactory {
	return openCodeFormatProviderFactory{
		def:  cloneAgentDef(def),
		spec: openCodeProviderSpecForAgent(AgentMiMoCode),
	}
}

func (f openCodeFormatProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f openCodeFormatProviderFactory) Capabilities() Capabilities {
	return openCodeFormatProviderCapabilities()
}

func (f openCodeFormatProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &openCodeFormatProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   openCodeFormatProviderCapabilities(),
			Config: cfg,
		},
		sources: newOpenCodeFormatSourceSet(cfg.Roots, f.spec),
	}
}

type openCodeFormatProvider struct {
	ProviderBase
	sources openCodeFormatSourceSet
}

func (p *openCodeFormatProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *openCodeFormatProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *openCodeFormatProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *openCodeFormatProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *openCodeFormatProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *openCodeFormatProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("%s source path unavailable", p.Def.Type)
	}

	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	var (
		sess *ParsedSession
		msgs []ParsedMessage
		err  error
	)
	if dbPath, sessionID, ok := p.sources.spec.parseVirtual(path); ok {
		sess, msgs, err = p.sources.spec.parseSQLite(dbPath, sessionID, machine)
	} else {
		sess, msgs, err = p.sources.spec.parseFile(path, machine)
	}
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

type openCodeProviderSpec struct {
	agent        AgentType
	dbName       string
	resolve      func(string) OpenCodeSource
	discover     func(string) []DiscoveredFile
	find         func(string, string) string
	watchRoots   func(string) []string
	storageIDs   func(string) map[string]struct{}
	listSQLite   func(string) ([]OpenCodeSessionMeta, error)
	sourceMtime  func(string) (int64, error)
	parseFile    func(string, string) (*ParsedSession, []ParsedMessage, error)
	parseSQLite  func(string, string, string) (*ParsedSession, []ParsedMessage, error)
	parseVirtual func(string) (string, string, bool)
}

func openCodeProviderSpecForAgent(agent AgentType) openCodeProviderSpec {
	switch agent {
	case AgentOpenCode:
		return openCodeProviderSpec{
			agent:        AgentOpenCode,
			dbName:       openCodeFmt.dbName,
			resolve:      ResolveOpenCodeSource,
			discover:     DiscoverOpenCodeSessions,
			find:         FindOpenCodeSourceFile,
			watchRoots:   ResolveOpenCodeWatchRoots,
			storageIDs:   OpenCodeStorageSessionIDs,
			listSQLite:   ListOpenCodeSessionMeta,
			sourceMtime:  OpenCodeSourceMtime,
			parseFile:    ParseOpenCodeFile,
			parseSQLite:  ParseOpenCodeSession,
			parseVirtual: ParseOpenCodeSQLiteVirtualPath,
		}
	case AgentKilo:
		return openCodeProviderSpec{
			agent:        AgentKilo,
			dbName:       kiloFmt.dbName,
			resolve:      ResolveKiloSource,
			discover:     DiscoverKiloSessions,
			find:         FindKiloSourceFile,
			watchRoots:   ResolveKiloWatchRoots,
			storageIDs:   KiloStorageSessionIDs,
			listSQLite:   ListKiloSessionMeta,
			sourceMtime:  KiloSourceMtime,
			parseFile:    ParseKiloFile,
			parseSQLite:  ParseKiloSession,
			parseVirtual: ParseKiloSQLiteVirtualPath,
		}
	case AgentMiMoCode:
		return openCodeProviderSpec{
			agent:        AgentMiMoCode,
			dbName:       mimoFmt.dbName,
			resolve:      ResolveMiMoCodeSource,
			discover:     DiscoverMiMoCodeSessions,
			find:         FindMiMoCodeSourceFile,
			watchRoots:   ResolveMiMoCodeWatchRoots,
			storageIDs:   MiMoCodeStorageSessionIDs,
			listSQLite:   ListMiMoCodeSessionMeta,
			sourceMtime:  MiMoCodeSourceMtime,
			parseFile:    ParseMiMoCodeFile,
			parseSQLite:  ParseMiMoCodeSession,
			parseVirtual: ParseMiMoCodeSQLiteVirtualPath,
		}
	default:
		return openCodeProviderSpec{}
	}
}

type openCodeFormatSource struct {
	Root string
	Path string
}

type openCodeFormatSourceSet struct {
	roots []string
	spec  openCodeProviderSpec
}

func newOpenCodeFormatSourceSet(
	roots []string,
	spec openCodeProviderSpec,
) openCodeFormatSourceSet {
	return openCodeFormatSourceSet{
		roots: cleanJSONLRoots(roots),
		spec:  spec,
	}
}

func (s openCodeFormatSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		src := s.spec.resolve(root)
		storageIDs := map[string]struct{}{}
		if src.Mode == OpenCodeSourceStorage {
			for _, file := range s.spec.discover(root) {
				source, ok := s.sourceRef(root, file.Path, false)
				if !ok {
					continue
				}
				source.ProjectHint = file.Project
				addJSONLSource(source, &sources, seen)
			}
			storageIDs = s.spec.storageIDs(root)
		}
		if src.DBPath == "" || !IsRegularFile(src.DBPath) {
			continue
		}
		dbSources, err := s.sqliteSources(ctx, root, src.DBPath, storageIDs)
		if err != nil {
			return nil, err
		}
		for _, source := range dbSources {
			addJSONLSource(source, &sources, seen)
		}
	}
	sortJSONLSources(sources)
	return sources, nil
}

func (s openCodeFormatSourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots))
	for _, root := range s.roots {
		for _, watchRoot := range s.spec.watchRoots(root) {
			roots = append(roots, WatchRoot{
				Path:      watchRoot,
				Recursive: true,
				IncludeGlobs: []string{
					"*.json",
					s.spec.dbName,
					s.spec.dbName + "-*",
				},
				DebounceKey: string(s.spec.agent) + ":opencode:" + watchRoot,
			})
		}
	}
	return WatchPlan{Roots: roots}, nil
}

func (s openCodeFormatSourceSet) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pathExists := true
	if _, err := os.Stat(req.Path); err != nil {
		if !os.IsNotExist(err) {
			return nil, nil
		}
		pathExists = false
	}
	for _, root := range s.roots {
		sources, ok, err := s.sourcesForChangedPathInRoot(
			ctx, root, req.Path, pathExists,
		)
		if err != nil || ok {
			return sources, err
		}
	}
	return nil, nil
}

func (s openCodeFormatSourceSet) FindSource(
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
			if source, ok := s.sourceRef(root, path, true); ok {
				return source, true, nil
			}
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
		if source, ok := s.sourceRef(root, path, false); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s openCodeFormatSourceSet) Fingerprint(
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
	mtime, err := s.spec.sourceMtime(path)
	if err != nil {
		return SourceFingerprint{}, err
	}
	fingerprint := SourceFingerprint{
		Key:     firstNonEmptyJSONLString(source.FingerprintKey, source.Key, path),
		MTimeNS: mtime,
	}
	if dbPath, _, ok := s.spec.parseVirtual(path); ok {
		info, err := os.Stat(dbPath)
		if err != nil {
			return SourceFingerprint{}, fmt.Errorf("stat %s: %w", dbPath, err)
		}
		fingerprint.Size = info.Size()
		return fingerprint, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return SourceFingerprint{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return SourceFingerprint{}, fmt.Errorf("stat %s: source is a directory", path)
	}
	fingerprint.Size = info.Size()
	fingerprint.Hash, err = openCodeProviderStorageFingerprint(path)
	if err != nil {
		return SourceFingerprint{}, err
	}
	return fingerprint, nil
}

func (s openCodeFormatSourceSet) pathFromSource(source SourceRef) (string, bool) {
	switch src := source.Opaque.(type) {
	case openCodeFormatSource:
		return src.Path, src.Path != ""
	case *openCodeFormatSource:
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
			if ref, ok := s.sourceRef(root, candidate, false); ok {
				src := ref.Opaque.(openCodeFormatSource)
				return src.Path, true
			}
		}
	}
	return "", false
}

func (s openCodeFormatSourceSet) sqliteSources(
	ctx context.Context,
	root string,
	dbPath string,
	storageIDs map[string]struct{},
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	metas, err := s.spec.listSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	sources := make([]SourceRef, 0, len(metas))
	for _, meta := range metas {
		if _, exists := storageIDs[meta.SessionID]; exists {
			continue
		}
		source, ok := s.sourceRef(root, meta.VirtualPath, false)
		if !ok {
			continue
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func (s openCodeFormatSourceSet) sourcesForChangedPathInRoot(
	ctx context.Context,
	root string,
	path string,
	pathExists bool,
) ([]SourceRef, bool, error) {
	rel, ok := relUnder(root, path)
	if !ok {
		return nil, false, nil
	}
	base := filepath.Base(rel)
	if rel == s.spec.dbName || strings.HasPrefix(base, s.spec.dbName+"-") {
		dbPath := filepath.Join(root, s.spec.dbName)
		if !IsRegularFile(dbPath) {
			return nil, true, nil
		}
		storageIDs := map[string]struct{}{}
		if s.spec.resolve(root).Mode == OpenCodeSourceStorage {
			storageIDs = s.spec.storageIDs(root)
		}
		sources, err := s.sqliteSources(ctx, root, dbPath, storageIDs)
		return sources, true, err
	}

	src := s.spec.resolve(root)
	if src.Mode != OpenCodeSourceStorage {
		return nil, false, nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	sessionSubdir := filepath.Base(src.SessionRoot)
	switch {
	case pathExists &&
		len(parts) == 4 &&
		parts[0] == "storage" &&
		parts[1] == sessionSubdir &&
		strings.HasSuffix(parts[3], ".json"):
		source, ok := s.sourceRef(root, path, false)
		if !ok {
			return nil, true, nil
		}
		return []SourceRef{source}, true, nil
	case len(parts) == 4 &&
		parts[0] == "storage" &&
		parts[1] == "message" &&
		strings.HasSuffix(parts[3], ".json"):
		source, ok := s.sourceForRawID(root, parts[2])
		if !ok {
			return nil, false, nil
		}
		return []SourceRef{source}, true, nil
	case len(parts) == 4 &&
		parts[0] == "storage" &&
		parts[1] == "part" &&
		strings.HasSuffix(parts[3], ".json"):
		sessionID := ""
		if pathExists {
			sessionID = readOpenCodeProviderStorageSessionID(path)
		}
		if sessionID == "" {
			sessionID = findOpenCodeProviderStorageSessionIDByMessageID(root, parts[2])
		}
		if sessionID == "" {
			return nil, false, nil
		}
		source, ok := s.sourceForRawID(root, sessionID)
		if !ok {
			return nil, false, nil
		}
		return []SourceRef{source}, true, nil
	case !pathExists &&
		len(parts) == 3 &&
		parts[0] == "storage" &&
		parts[1] == "message":
		source, ok := s.sourceForRawID(root, parts[2])
		if !ok {
			return nil, false, nil
		}
		return []SourceRef{source}, true, nil
	case !pathExists &&
		len(parts) == 3 &&
		parts[0] == "storage" &&
		parts[1] == "part":
		sessionID := findOpenCodeProviderStorageSessionIDByMessageID(root, parts[2])
		if sessionID == "" {
			return nil, false, nil
		}
		source, ok := s.sourceForRawID(root, sessionID)
		if !ok {
			return nil, false, nil
		}
		return []SourceRef{source}, true, nil
	}
	return nil, false, nil
}

func (s openCodeFormatSourceSet) sourceForRawID(root, sessionID string) (SourceRef, bool) {
	path := s.spec.find(root, sessionID)
	if path == "" {
		return SourceRef{}, false
	}
	return s.sourceRef(root, path, false)
}

func (s openCodeFormatSourceSet) sourceRef(
	root string,
	path string,
	promoteVirtual bool,
) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if dbPath, sessionID, ok := s.spec.parseVirtual(path); ok {
		if _, under := relUnder(root, dbPath); !under {
			return SourceRef{}, false
		}
		if promoteVirtual {
			if selected := s.spec.find(root, sessionID); selected != "" &&
				selected != path {
				return s.sourceRef(root, selected, false)
			}
		}
		if !OpenCodeSQLiteSessionExists(dbPath, sessionID) {
			return SourceRef{}, false
		}
		return s.newSourceRef(root, path, ""), true
	}
	if !s.isStorageSessionPath(root, path) {
		return SourceRef{}, false
	}
	return s.newSourceRef(root, path, openCodeSessionProject(path)), true
}

func (s openCodeFormatSourceSet) newSourceRef(
	root string,
	path string,
	project string,
) SourceRef {
	return SourceRef{
		Provider:       s.spec.agent,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		ProjectHint:    project,
		Opaque: openCodeFormatSource{
			Root: root,
			Path: path,
		},
	}
}

func (s openCodeFormatSourceSet) isStorageSessionPath(root, path string) bool {
	rel, ok := relUnder(root, path)
	if !ok {
		return false
	}
	src := s.spec.resolve(root)
	if src.Mode != OpenCodeSourceStorage {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 4 &&
		parts[0] == "storage" &&
		parts[1] == filepath.Base(src.SessionRoot) &&
		strings.HasSuffix(parts[3], ".json") &&
		IsRegularFile(path)
}

func openCodeProviderStorageFingerprint(sessionPath string) (string, error) {
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(sessionPath))))
	sessionID := strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
	msgs, err := loadOpenCodeStorageMessages(root, sessionID)
	if err != nil {
		return "", err
	}
	parts, err := loadOpenCodeStorageParts(root, msgs)
	if err != nil {
		return "", err
	}
	return buildOpenCodeStorageFingerprint(msgs, parts), nil
}

func readOpenCodeProviderStorageSessionID(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var data struct {
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return ""
	}
	return data.SessionID
}

func findOpenCodeProviderStorageSessionIDByMessageID(
	openCodeDir, messageID string,
) string {
	messageRoot := filepath.Join(openCodeDir, "storage", "message")
	entries, err := os.ReadDir(messageRoot)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(messageRoot, entry.Name(), messageID+".json")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return entry.Name()
		}
	}
	return ""
}

func openCodeFormatProviderCapabilities() Capabilities {
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
			Cwd:                  CapabilitySupported,
			Relationships:        CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
