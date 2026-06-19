package parser

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var _ Provider = (*cortexProvider)(nil)

type cortexProviderFactory struct {
	def AgentDef
}

func newCortexProviderFactory(def AgentDef) ProviderFactory {
	return cortexProviderFactory{def: cloneAgentDef(def)}
}

func (f cortexProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f cortexProviderFactory) Capabilities() Capabilities {
	return cortexProviderCapabilities()
}

func (f cortexProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &cortexProvider{
		ProviderBase: ProviderBase{
			Def:    cloneAgentDef(f.def),
			Caps:   cortexProviderCapabilities(),
			Config: cfg,
		},
		sources: newCortexSourceSet(cfg.Roots),
	}
}

type cortexProvider struct {
	ProviderBase
	sources JSONLSourceSet
}

func (p *cortexProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *cortexProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	plan, err := p.sources.WatchPlan(ctx)
	if err != nil {
		return WatchPlan{}, err
	}
	for i := range plan.Roots {
		plan.Roots[i].IncludeGlobs = append(
			plan.Roots[i].IncludeGlobs,
			"*.history.jsonl",
		)
	}
	return plan, nil
}

func (p *cortexProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	sources, err := p.sources.SourcesForChangedPath(ctx, req)
	if err != nil || len(sources) > 0 {
		return sources, err
	}
	if source, ok := p.sourceForHistoryCompanion(req); ok {
		return []SourceRef{source}, nil
	}
	return nil, nil
}

func (p *cortexProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	return p.sources.FindSource(ctx, providerFindRequestWithRawSessionID(p.Def, req))
}

func (p *cortexProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	path, ok := p.sources.pathFromSource(source)
	if !ok {
		return SourceFingerprint{}, fmt.Errorf("cortex source path unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return SourceFingerprint{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return SourceFingerprint{}, fmt.Errorf("stat %s: source is a directory", path)
	}
	fingerprint := SourceFingerprint{
		Key: firstNonEmptyJSONLString(
			source.FingerprintKey,
			source.Key,
			path,
		),
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
	}

	h := sha256.New()
	if err := addCortexFingerprintPart(h, "metadata", path, info); err != nil {
		return SourceFingerprint{}, err
	}
	historyPath := cortexHistoryCompanionPath(path)
	if historyInfo, ok, err := cortexCompanionInfo(historyPath); err != nil {
		return SourceFingerprint{}, err
	} else if ok && historyInfo != nil {
		fingerprint.Size += historyInfo.Size()
		mtime := historyInfo.ModTime().UnixNano()
		if mtime > fingerprint.MTimeNS {
			fingerprint.MTimeNS = mtime
		}
		if err := addCortexFingerprintPart(h, "history", historyPath, historyInfo); err != nil {
			return SourceFingerprint{}, err
		}
	}
	fingerprint.Hash = fmt.Sprintf("%x", h.Sum(nil))
	return fingerprint, nil
}

func (p *cortexProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	path, ok := p.sources.pathFromSource(req.Source)
	if !ok {
		return ParseOutcome{}, fmt.Errorf("cortex source path unavailable")
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, err := ParseCortexSession(path, machine)
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

func newCortexSourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentCortex, roots, JSONLSourceSetOptions{
		Extensions:         []string{".json"},
		FollowSymlinkFiles: true,
		IncludePath:        isCortexSourcePath,
		SessionIDFromPath:  cortexSessionIDFromPath,
		ProjectHint:        func(root, path string) string { return "" },
	})
}

func (p *cortexProvider) sourceForHistoryCompanion(
	req ChangedPathRequest,
) (SourceRef, bool) {
	if req.Path == "" {
		return SourceRef{}, false
	}
	path := filepath.Clean(req.Path)
	for _, root := range p.sources.roots {
		if req.WatchRoot != "" && !samePath(req.WatchRoot, root) {
			continue
		}
		source, ok := cortexSourceForHistoryCompanion(p.sources, root, path)
		if ok {
			return source, true
		}
	}
	return SourceRef{}, false
}

func cortexSourceForHistoryCompanion(
	sources JSONLSourceSet,
	root string,
	path string,
) (SourceRef, bool) {
	root = filepath.Clean(root)
	if !samePath(filepath.Dir(path), root) {
		return SourceRef{}, false
	}
	stem, ok := strings.CutSuffix(filepath.Base(path), ".history.jsonl")
	if !ok || !IsCortexSessionFile(stem+".json") {
		return SourceRef{}, false
	}
	metadataPath := filepath.Join(root, stem+".json")
	if source, ok := sources.sourceForPath(metadataPath); ok {
		return source, true
	}
	return SourceRef{}, false
}

func isCortexSourcePath(root, path string) bool {
	if !samePath(filepath.Dir(path), filepath.Clean(root)) {
		return false
	}
	return IsCortexSessionFile(filepath.Base(path))
}

func cortexSessionIDFromPath(root, path string) string {
	if !isCortexSourcePath(root, path) {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".json")
}

func cortexHistoryCompanionPath(path string) string {
	return strings.TrimSuffix(path, ".json") + ".history.jsonl"
}

func cortexCompanionInfo(path string) (os.FileInfo, bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, false, nil
	}
	return info, true, nil
}

func addCortexFingerprintPart(
	h interface{ Write([]byte) (int, error) },
	label string,
	path string,
	info os.FileInfo,
) error {
	hash, err := hashJSONLSourceFile(path)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(
		h,
		"%s:%s:%d:%d:%s\n",
		label,
		filepath.Base(path),
		info.Size(),
		info.ModTime().UnixNano(),
		hash,
	)
	return nil
}

func cortexProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage: CapabilitySupported,
			SessionName:  CapabilitySupported,
			Cwd:          CapabilitySupported,
			ToolCalls:    CapabilitySupported,
			ToolResults:  CapabilitySupported,
		},
	}
}
