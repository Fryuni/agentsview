package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var _ Provider = (*coworkProvider)(nil)

type coworkProviderFactory struct {
	def AgentDef
}

func newCoworkProviderFactory(def AgentDef) ProviderFactory {
	return coworkProviderFactory{def: cloneAgentDef(def)}
}

func (f coworkProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f coworkProviderFactory) Capabilities() Capabilities {
	return coworkProviderCapabilities()
}

func (f coworkProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &coworkProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   coworkProviderCapabilities(),
			Config: cfg,
		},
		sources: newCoworkSourceSet(cfg.Roots),
	}
}

type coworkProvider struct {
	ProviderBase
	sources coworkSourceSet
}

func (p *coworkProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *coworkProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *coworkProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *coworkProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *coworkProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *coworkProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("cowork source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	results, excludedIDs, err := ParseCoworkSession(path, machine)
	if err != nil {
		return ParseOutcome{}, err
	}
	if req.Fingerprint.Hash != "" {
		for i := range results {
			results[i].Session.File.Hash = req.Fingerprint.Hash
		}
	}
	out := make([]ParseResultOutcome, 0, len(results))
	for _, result := range results {
		out = append(out, ParseResultOutcome{
			Result:      result,
			DataVersion: DataVersionCurrent,
		})
	}
	return ParseOutcome{
		Results:            out,
		ExcludedSessionIDs: excludedIDs,
		ResultSetComplete:  true,
	}, nil
}

type coworkSource struct {
	Root string
	Path string
}

type coworkSourceSet struct {
	roots []string
}

func newCoworkSourceSet(roots []string) coworkSourceSet {
	return coworkSourceSet{roots: cleanJSONLRoots(roots)}
}

func (s coworkSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverCoworkSessions(root) {
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

func (s coworkSourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots))
	for _, root := range s.roots {
		roots = append(roots, WatchRoot{
			Path:         root,
			Recursive:    false,
			IncludeGlobs: []string{"local_*.json"},
			DebounceKey:  string(AgentCowork) + ":metadata:" + root,
		})
	}
	return WatchPlan{Roots: roots}, nil
}

func (s coworkSourceSet) SourcesForChangedPath(
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
		source, ok := s.sourceForChangedPath(root, req.Path)
		if !ok {
			return nil, nil
		}
		return []SourceRef{source}, nil
	}
	for _, root := range s.roots {
		source, ok := s.sourceForChangedPath(root, req.Path)
		if ok {
			return []SourceRef{source}, nil
		}
	}
	return nil, nil
}

func (s coworkSourceSet) FindSource(
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
		path := FindCoworkSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s coworkSourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	path, ok := s.pathFromSource(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("cowork source path unavailable")
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
		MTimeNS: CoworkSessionMtime(path, info.ModTime().UnixNano()),
		Hash:    hash,
	}, nil
}

func (s coworkSourceSet) pathFromSource(source SourceRef) (string, bool) {
	switch src := source.Opaque.(type) {
	case coworkSource:
		return src.Path, src.Path != ""
	case *coworkSource:
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
				src := ref.Opaque.(coworkSource)
				return src.Path, true
			}
		}
	}
	return "", false
}

func (s coworkSourceSet) sourceForChangedPath(root, path string) (SourceRef, bool) {
	transcript, ok := ClassifyCoworkPath(root, path)
	if !ok {
		return SourceRef{}, false
	}
	return s.sourceRef(root, transcript)
}

func (s coworkSourceSet) sourceForPath(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if transcript, ok := ClassifyCoworkPath(root, path); ok {
		return s.sourceRef(root, transcript)
	}
	return SourceRef{}, false
}

func (s coworkSourceSet) sourceRef(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if _, ok := relUnder(root, path); !ok {
		return SourceRef{}, false
	}
	if coworkMetaPathForTranscript(path) == "" {
		return SourceRef{}, false
	}
	if !isCoworkTranscriptPath(root, path) {
		return SourceRef{}, false
	}
	return SourceRef{
		Provider:       AgentCowork,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		ProjectHint:    coworkProjectName(readCoworkMeta(coworkMetaPathForTranscript(path))),
		Opaque: coworkSource{
			Root: root,
			Path: path,
		},
	}, true
}

func (s coworkSourceSet) hasRoot(root string) bool {
	for _, configured := range s.roots {
		if samePath(root, configured) {
			return true
		}
	}
	return false
}

func isCoworkTranscriptPath(root, path string) bool {
	rel, ok := relUnder(root, path)
	if !ok || filepath.Ext(path) != ".jsonl" {
		return false
	}
	sep := string(filepath.Separator)
	parts := strings.Split(rel, sep)
	n := len(parts)
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if n >= 5 && parts[n-4] == ".claude" && parts[n-3] == "projects" {
		return IsValidSessionID(base)
	}
	if !strings.Contains(sep+rel, sep+".claude"+sep+"projects"+sep) ||
		!slices.Contains(parts, "subagents") {
		return false
	}
	return strings.HasPrefix(base, "agent-")
}

func coworkProviderCapabilities() Capabilities {
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
			ExcludedSessions:     CapabilitySupported,
			ForceReplaceOnParse:  CapabilityNotApplicable,
		},
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			SessionName:          CapabilitySupported,
			Cwd:                  CapabilitySupported,
			GitBranch:            CapabilitySupported,
			Relationships:        CapabilitySupported,
			Subagents:            CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			TerminationStatus:    CapabilitySupported,
			MalformedLineCount:   CapabilitySupported,
			Model:                CapabilitySupported,
			StopReason:           CapabilitySupported,
		},
	}
}
