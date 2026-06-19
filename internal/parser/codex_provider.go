package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

var _ Provider = (*codexProvider)(nil)

type codexProviderFactory struct {
	def AgentDef
}

func newCodexProviderFactory(def AgentDef) ProviderFactory {
	return codexProviderFactory{def: cloneAgentDef(def)}
}

func (f codexProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f codexProviderFactory) Capabilities() Capabilities {
	return codexProviderCapabilities()
}

func (f codexProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &codexProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   codexProviderCapabilities(),
			Config: cfg,
		},
		sources: newCodexSourceSet(cfg.Roots),
	}
}

type codexProvider struct {
	ProviderBase
	sources codexSourceSet
}

func (p *codexProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *codexProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *codexProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *codexProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = providerFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *codexProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

func (p *codexProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("codex source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseCodexSession(path, machine, false)
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

func (p *codexProvider) ParseIncremental(
	ctx context.Context,
	req IncrementalRequest,
) (IncrementalOutcome, IncrementalStatus, error) {
	if err := ctx.Err(); err != nil {
		return IncrementalOutcome{}, IncrementalUnsupported, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return IncrementalOutcome{}, IncrementalUnsupported,
			fmt.Errorf("codex source path unavailable")
	}
	if req.Fingerprint.Size > 0 && req.Fingerprint.Size <= req.Offset {
		return IncrementalOutcome{}, IncrementalNoNewData, nil
	}
	newMsgs, endedAt, consumed, err := ParseCodexSessionFrom(
		path,
		req.Offset,
		req.StartOrdinal,
		false,
	)
	if err != nil {
		if IsIncrementalFullParseFallback(err) {
			return IncrementalOutcome{ForceReplace: true},
				IncrementalNeedsFullParse, nil
		}
		return IncrementalOutcome{}, IncrementalNeedsFullParse, err
	}
	if len(newMsgs) == 0 {
		if consumed > 0 {
			return IncrementalOutcome{
				SessionID:     req.SessionID,
				EndedAt:       endedAt,
				ConsumedBytes: consumed,
			}, IncrementalApplied, nil
		}
		return IncrementalOutcome{}, IncrementalNoNewData, nil
	}
	totalOut, peakCtx, hasTotalOut, hasPeakCtx := codexProviderTokenTotals(newMsgs)
	return IncrementalOutcome{
		SessionID:            req.SessionID,
		Messages:             newMsgs,
		EndedAt:              endedAt,
		ConsumedBytes:        consumed,
		MessageCount:         len(newMsgs),
		UserMessageCount:     codexProviderUserMessageCount(newMsgs),
		TotalOutputTokens:    totalOut,
		PeakContextTokens:    peakCtx,
		HasTotalOutputTokens: hasTotalOut,
		HasPeakContextTokens: hasPeakCtx,
	}, IncrementalApplied, nil
}

type codexSource struct {
	Root string
	Path string
}

type codexSourceSet struct {
	roots []string
}

func newCodexSourceSet(roots []string) codexSourceSet {
	return codexSourceSet{roots: cleanJSONLRoots(roots)}
}

func (s codexSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, file := range DiscoverCodexSessions(root) {
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

func (s codexSourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots)*2)
	seenShallow := make(map[string]struct{})
	for _, root := range s.roots {
		roots = append(roots, WatchRoot{
			Path:         root,
			Recursive:    true,
			IncludeGlobs: []string{"*.jsonl"},
			DebounceKey:  string(AgentCodex) + ":sessions:" + root,
		})
		for _, shallow := range ResolveCodexShallowWatchRoots(root) {
			shallow = filepath.Clean(shallow)
			if _, ok := seenShallow[shallow]; ok {
				continue
			}
			seenShallow[shallow] = struct{}{}
			roots = append(roots, WatchRoot{
				Path:         shallow,
				Recursive:    false,
				IncludeGlobs: []string{CodexSessionIndexFilename},
				DebounceKey:  string(AgentCodex) + ":index:" + shallow,
			})
		}
	}
	return WatchPlan{Roots: roots}, nil
}

func (s codexSourceSet) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filepath.Base(req.Path) == CodexSessionIndexFilename {
		return s.sourcesForIndexPath(ctx, req.Path)
	}
	for _, root := range s.roots {
		source, ok := s.sourceRef(root, req.Path)
		if ok {
			return []SourceRef{source}, nil
		}
	}
	return nil, nil
}

func (s codexSourceSet) FindSource(
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
		path := FindCodexSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(root, path); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s codexSourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	path, ok := s.pathFromSource(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("codex source path unavailable")
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
		MTimeNS: CodexEffectiveMtime(path, info.ModTime().UnixNano()),
		Hash:    hash,
	}, nil
}

func (s codexSourceSet) pathFromSource(source SourceRef) (string, bool) {
	switch src := source.Opaque.(type) {
	case codexSource:
		return src.Path, src.Path != ""
	case *codexSource:
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
			if ref, ok := s.sourceRef(root, candidate); ok {
				src := ref.Opaque.(codexSource)
				return src.Path, true
			}
		}
	}
	return "", false
}

func (s codexSourceSet) sourcesForIndexPath(
	ctx context.Context,
	indexPath string,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	titles := CodexSessionIndexTitles(indexPath)
	if len(titles) == 0 {
		return nil, nil
	}
	indexDir := filepath.Dir(indexPath)
	var sources []SourceRef
	seen := make(map[string]struct{})
	ids := make([]string, 0, len(titles))
	for id := range titles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, root := range s.roots {
		if filepath.Dir(root) != indexDir {
			continue
		}
		for _, id := range ids {
			path := FindCodexSourceFile(root, id)
			if path == "" {
				continue
			}
			source, ok := s.sourceRef(root, path)
			if !ok {
				continue
			}
			addJSONLSource(source, &sources, seen)
		}
	}
	sortJSONLSources(sources)
	return sources, nil
}

func (s codexSourceSet) sourceRef(root, path string) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if _, _, ok := CodexSessionPathInfo(root, path); !ok {
		return SourceRef{}, false
	}
	if !IsRegularFile(path) {
		return SourceRef{}, false
	}
	return SourceRef{
		Provider:       AgentCodex,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		Opaque: codexSource{
			Root: root,
			Path: path,
		},
	}, true
}

func codexProviderUserMessageCount(msgs []ParsedMessage) int {
	count := 0
	for _, msg := range msgs {
		if msg.Role == RoleUser && !msg.IsSystem && len(msg.ToolResults) == 0 {
			count++
		}
	}
	return count
}

func codexProviderTokenTotals(
	msgs []ParsedMessage,
) (totalOut int, peakCtx int, hasTotalOut bool, hasPeakCtx bool) {
	for _, msg := range msgs {
		msgHasCtx, msgHasOut := msg.TokenPresence()
		if msgHasOut {
			totalOut += msg.OutputTokens
			hasTotalOut = true
		}
		if msgHasCtx && (!hasPeakCtx || msg.ContextTokens > peakCtx) {
			peakCtx = msg.ContextTokens
			hasPeakCtx = true
		}
	}
	return totalOut, peakCtx, hasTotalOut, hasPeakCtx
}

func codexProviderCapabilities() Capabilities {
	return Capabilities{
		Source: SourceCapabilities{
			DiscoverSources:      CapabilitySupported,
			WatchSources:         CapabilitySupported,
			ClassifyChangedPath:  CapabilitySupported,
			FindSource:           CapabilitySupported,
			CompositeFingerprint: CapabilitySupported,
			IncrementalAppend:    CapabilitySupported,
			MultiSessionSource:   CapabilityNotApplicable,
			PerSessionErrors:     CapabilityNotApplicable,
			ExcludedSessions:     CapabilityNotApplicable,
			ForceReplaceOnParse:  CapabilitySupported,
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
			ToolResultEvents:     CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			TerminationStatus:    CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
