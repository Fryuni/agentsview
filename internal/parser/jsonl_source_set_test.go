package parser

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONLSourceSetDiscoverRecursiveStableSources(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, filepath.Join(root, "b.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(root, "a.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(root, "nested", "c.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(root, "nested", "ignored.txt"), "{}\n")
	writeSourceFile(t, filepath.Join(root, "nested", "upper.JSONL"), "{}\n")

	roots := []string{root}
	sources := NewJSONLSourceSet(AgentCodex, roots, JSONLSourceSetOptions{
		Recursive: true,
		Key: func(root, path string) string {
			return mustRelSlash(t, root, path)
		},
		ProjectHint: func(root, path string) string {
			rel := mustRelSlash(t, root, filepath.Dir(path))
			if rel == "." {
				return ""
			}
			return rel
		},
	})
	roots[0] = filepath.Join(root, "mutated")

	discovered, err := sources.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, discovered, 3)

	assert.Equal(t, []string{
		"a.jsonl",
		"b.jsonl",
		"nested/c.jsonl",
	}, sourceKeys(discovered))
	assert.Equal(t, []string{"", "", "nested"}, sourceProjects(discovered))
	for _, source := range discovered {
		assert.Equal(t, AgentCodex, source.Provider)
		assert.Equal(t, source.DisplayPath, source.FingerprintKey)
		assert.NotEmpty(t, source.DisplayPath)
		assert.IsType(t, JSONLSource{}, source.Opaque)
	}
}

func TestJSONLSourceSetShallowDiscoveryAndFilters(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, filepath.Join(root, "keep.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(root, "keep.ndjson"), "{}\n")
	writeSourceFile(t, filepath.Join(root, "drop.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(root, "nested", "skip.jsonl"), "{}\n")

	sources := NewJSONLSourceSet(AgentGptme, []string{root}, JSONLSourceSetOptions{
		Extensions: []string{".jsonl", ".ndjson"},
		Include: func(path string, _ os.FileInfo) bool {
			return filepath.Base(path) != "drop.jsonl"
		},
	})

	discovered, err := sources.Discover(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{
		filepath.Join(root, "keep.jsonl"),
		filepath.Join(root, "keep.ndjson"),
	}, sourceDisplayPaths(discovered))
}

func TestJSONLSourceSetWatchChangedPathFindAndFingerprint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "session-1.jsonl")
	content := "{\"role\":\"user\"}\n"
	writeSourceFile(t, path, content)
	writeSourceFile(t, filepath.Join(root, "nested", "notes.txt"), "{}\n")

	sources := NewJSONLSourceSet(AgentCodex, []string{root}, JSONLSourceSetOptions{
		Recursive: true,
		Hash:      true,
	})

	plan, err := sources.WatchPlan(context.Background())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 1)
	assert.Equal(t, root, plan.Roots[0].Path)
	assert.True(t, plan.Roots[0].Recursive)
	assert.Equal(t, []string{"*.jsonl"}, plan.Roots[0].IncludeGlobs)
	assert.NotEmpty(t, plan.Roots[0].DebounceKey)

	changed, err := sources.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{Path: path, EventKind: "write", WatchRoot: root},
	)
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, path, changed[0].Key)
	assert.Equal(t, path, changed[0].DisplayPath)
	assert.Equal(t, path, changed[0].FingerprintKey)

	ignored, err := sources.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{
			Path:      filepath.Join(root, "nested", "notes.txt"),
			EventKind: "write",
			WatchRoot: root,
		},
	)
	require.NoError(t, err)
	assert.Empty(t, ignored)

	outside, err := sources.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{
			Path:      filepath.Join(t.TempDir(), "session-1.jsonl"),
			EventKind: "write",
			WatchRoot: root,
		},
	)
	require.NoError(t, err)
	assert.Empty(t, outside)

	found, ok, err := sources.FindSource(context.Background(), FindSourceRequest{
		StoredFilePath: path,
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, path, found.DisplayPath)

	foundByID, ok, err := sources.FindSource(context.Background(), FindSourceRequest{
		RawSessionID: "session-1",
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, found.DisplayPath, foundByID.DisplayPath)

	withoutOpaque := found
	withoutOpaque.Opaque = nil
	fingerprint, err := sources.Fingerprint(context.Background(), withoutOpaque)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, path, fingerprint.Key)
	assert.Equal(t, info.Size(), fingerprint.Size)
	assert.Equal(t, info.ModTime().UnixNano(), fingerprint.MTimeNS)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(content))), fingerprint.Hash)
}

func TestJSONLSourceSetMissingRootAndInvalidLookupAreNoops(t *testing.T) {
	root := t.TempDir()
	sources := NewJSONLSourceSet(AgentCodex, []string{
		filepath.Join(root, "missing"),
	}, JSONLSourceSetOptions{})

	discovered, err := sources.Discover(context.Background())
	require.NoError(t, err)
	assert.Empty(t, discovered)

	found, ok, err := sources.FindSource(context.Background(), FindSourceRequest{
		RawSessionID: "../session",
	})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, found)
}

func writeSourceFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func mustRelSlash(t *testing.T, root, path string) string {
	t.Helper()

	rel, err := filepath.Rel(root, path)
	require.NoError(t, err)
	return filepath.ToSlash(rel)
}

func sourceKeys(sources []SourceRef) []string {
	keys := make([]string, 0, len(sources))
	for _, source := range sources {
		keys = append(keys, source.Key)
	}
	return keys
}

func sourceProjects(sources []SourceRef) []string {
	projects := make([]string, 0, len(sources))
	for _, source := range sources {
		projects = append(projects, source.ProjectHint)
	}
	return projects
}

func sourceDisplayPaths(sources []SourceRef) []string {
	paths := make([]string, 0, len(sources))
	for _, source := range sources {
		paths = append(paths, source.DisplayPath)
	}
	return paths
}
