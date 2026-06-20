package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZedProviderFactoryReplacesLegacyAdapter(t *testing.T) {
	factory, ok := ProviderFactoryByType(AgentZed)
	require.True(t, ok)
	caps := factory.Capabilities()
	assert.Equal(t, CapabilityUnsupported, caps.Content.Relationships)
	assert.Equal(t, CapabilitySupported, caps.Content.AggregateUsageEvents)

	provider, ok := NewProvider(AgentZed, ProviderConfig{
		Roots:   []string{t.TempDir()},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)
}

func TestZedProviderSourceMethods(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, zedThreadsDBRelPath)
	threadID := "10431c84-c47b-4e6c-b2df-f9f3b9ad025b"
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	createZedThreadsDBAt(t, dbPath, []zedTestThread{{
		id:        threadID,
		summary:   "Provider thread",
		createdAt: "2026-06-08T09:12:41Z",
		updatedAt: "2026-06-08T09:14:10Z",
		dataType:  "json",
		data:      []byte(`{"messages":[{"User":{"content":[{"Text":"Hello Zed"}]}}]}`),
	}})
	virtualPath := ZedSQLiteVirtualPath(dbPath, threadID)

	provider, ok := NewProvider(AgentZed, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)

	plan, err := provider.WatchPlan(context.Background())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 1)
	assert.Equal(t, filepath.Join(root, "threads"), plan.Roots[0].Path)
	assert.False(t, plan.Roots[0].Recursive)
	assert.Equal(t, []string{"threads.db", "threads.db-*"}, plan.Roots[0].IncludeGlobs)

	discovered, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	assert.Equal(t, AgentZed, discovered[0].Provider)
	assert.Equal(t, dbPath, discovered[0].DisplayPath)
	assert.Equal(t, dbPath, discovered[0].FingerprintKey)

	found, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		FullSessionID: "host~zed:" + threadID,
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, virtualPath, found.DisplayPath)
	assert.Equal(t, virtualPath, found.FingerprintKey)

	fingerprint, err := provider.Fingerprint(context.Background(), found)
	require.NoError(t, err)
	assert.Equal(t, virtualPath, fingerprint.Key)
	assert.Positive(t, fingerprint.Size)
	assert.NotZero(t, fingerprint.MTimeNS)
	assert.NotEmpty(t, fingerprint.Hash)

	changed, err := provider.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{Path: dbPath + "-wal", EventKind: "write", WatchRoot: filepath.Dir(dbPath)},
	)
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, dbPath, changed[0].DisplayPath)
}

func TestZedProviderParsePhysicalAndVirtualSources(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, zedThreadsDBRelPath)
	threadOne := "10431c84-c47b-4e6c-b2df-f9f3b9ad025b"
	threadTwo := "20431c84-c47b-4e6c-b2df-f9f3b9ad025b"
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	createZedThreadsDBAt(t, dbPath, []zedTestThread{
		{
			id:        threadOne,
			summary:   "First thread",
			createdAt: "2026-06-08T09:12:41Z",
			updatedAt: "2026-06-08T09:14:10Z",
			dataType:  "json",
			data:      []byte(`{"messages":[{"User":{"content":[{"Text":"First"}]}}]}`),
		},
		{
			id:        threadTwo,
			summary:   "Second thread",
			createdAt: "2026-06-08T09:15:41Z",
			updatedAt: "2026-06-08T09:16:10Z",
			dataType:  "json",
			data:      []byte(`{"messages":[{"User":{"content":[{"Text":"Second"}]}}]}`),
		},
	})

	provider, ok := NewProvider(AgentZed, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)
	sources, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, sources, 1)

	allOutcome, err := provider.Parse(context.Background(), ParseRequest{
		Source: sources[0],
	})
	require.NoError(t, err)
	require.True(t, allOutcome.ResultSetComplete)
	require.True(t, allOutcome.ForceReplace)
	require.Len(t, allOutcome.Results, 2)
	assert.Equal(t, "zed:"+threadOne, allOutcome.Results[0].Result.Session.ID)
	assert.Equal(t, "zed:"+threadTwo, allOutcome.Results[1].Result.Session.ID)

	virtualSource, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		RawSessionID: threadTwo,
	})
	require.NoError(t, err)
	require.True(t, ok)
	oneOutcome, err := provider.Parse(context.Background(), ParseRequest{
		Source: virtualSource,
	})
	require.NoError(t, err)
	require.True(t, oneOutcome.ResultSetComplete)
	require.True(t, oneOutcome.ForceReplace)
	require.Len(t, oneOutcome.Results, 1)
	assert.Equal(t, "zed:"+threadTwo, oneOutcome.Results[0].Result.Session.ID)
	assert.Equal(t, "devbox", oneOutcome.Results[0].Result.Session.Machine)
	assert.Len(t, oneOutcome.Results[0].Result.Messages, 1)
}

func TestZedProviderFingerprintIncludesWALSiblings(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, zedThreadsDBRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	createZedThreadsDBAt(t, dbPath, []zedTestThread{{
		id:        "10431c84-c47b-4e6c-b2df-f9f3b9ad025b",
		summary:   "Provider thread",
		updatedAt: "2026-06-08T09:14:10Z",
		dataType:  "json",
		data:      []byte(`{"messages":[{"User":{"content":[{"Text":"Hello Zed"}]}}]}`),
	}})

	provider, ok := NewProvider(AgentZed, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	before, err := provider.Fingerprint(context.Background(), sources[0])
	require.NoError(t, err)

	walPath := dbPath + "-wal"
	writeSourceFile(t, walPath, "wal")
	walTime := time.Unix(0, before.MTimeNS+int64(time.Second))
	require.NoError(t, os.Chtimes(walPath, walTime, walTime))
	after, err := provider.Fingerprint(context.Background(), sources[0])
	require.NoError(t, err)

	assert.Equal(t, before.Size, after.Size)
	assert.Greater(t, after.MTimeNS, before.MTimeNS)
}

func TestZedProviderClassifiesDeletedPhysicalDB(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, zedThreadsDBRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	createZedThreadsDBAt(t, dbPath, []zedTestThread{{
		id:        "10431c84-c47b-4e6c-b2df-f9f3b9ad025b",
		summary:   "Provider thread",
		updatedAt: "2026-06-08T09:14:10Z",
		dataType:  "json",
		data:      []byte(`{"messages":[{"User":{"content":[{"Text":"Hello Zed"}]}}]}`),
	}})
	require.NoError(t, os.Remove(dbPath))

	provider, ok := NewProvider(AgentZed, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	changed, err := provider.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{Path: dbPath, EventKind: "remove", WatchRoot: filepath.Dir(dbPath)},
	)
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, dbPath, changed[0].DisplayPath)

	outcome, err := provider.Parse(context.Background(), ParseRequest{Source: changed[0]})
	require.NoError(t, err)
	assert.True(t, outcome.ResultSetComplete)
	assert.True(t, outcome.ForceReplace)
	assert.Equal(t, SkipNoSession, outcome.SkipReason)
	assert.Empty(t, outcome.Results)
}

func TestZedProviderIgnoresUnrelatedSidecarBasename(t *testing.T) {
	root := t.TempDir()
	provider, ok := NewProvider(AgentZed, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)

	changed, err := provider.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{
			Path:      filepath.Join(root, "other", "threads.db-wal"),
			EventKind: "remove",
			WatchRoot: filepath.Join(root, "other"),
		},
	)
	require.NoError(t, err)
	assert.Empty(t, changed)
}

func TestShelleyProviderFactoryReplacesLegacyAdapter(t *testing.T) {
	factory, ok := ProviderFactoryByType(AgentShelley)
	require.True(t, ok)
	caps := factory.Capabilities()
	assert.Equal(t, CapabilitySupported, caps.Content.Relationships)
	assert.Equal(t, CapabilityUnsupported, caps.Content.AggregateUsageEvents)

	provider, ok := NewProvider(AgentShelley, ProviderConfig{
		Roots:   []string{t.TempDir()},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)
}

func TestShelleyProviderSourceMethods(t *testing.T) {
	root, dbPath, db := newShelleyTestDB(t)
	seedShelleyMainConversation(t, db)
	virtualPath := ShelleyVirtualPath(dbPath, "cMAIN1")

	provider, ok := NewProvider(AgentShelley, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)

	plan, err := provider.WatchPlan(context.Background())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 1)
	assert.Equal(t, root, plan.Roots[0].Path)
	assert.False(t, plan.Roots[0].Recursive)
	assert.Equal(t, []string{shelleyDBName, shelleyDBName + "-*"}, plan.Roots[0].IncludeGlobs)

	discovered, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	assert.Equal(t, AgentShelley, discovered[0].Provider)
	assert.Equal(t, dbPath, discovered[0].DisplayPath)
	assert.Equal(t, dbPath, discovered[0].FingerprintKey)

	found, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		FullSessionID: "host~shelley:cMAIN1",
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, virtualPath, found.DisplayPath)
	assert.Equal(t, virtualPath, found.FingerprintKey)

	fingerprint, err := provider.Fingerprint(context.Background(), found)
	require.NoError(t, err)
	assert.Equal(t, virtualPath, fingerprint.Key)
	assert.Positive(t, fingerprint.Size)
	assert.NotZero(t, fingerprint.MTimeNS)
	assert.NotEmpty(t, fingerprint.Hash)

	changed, err := provider.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{Path: dbPath + "-shm", EventKind: "write", WatchRoot: root},
	)
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, dbPath, changed[0].DisplayPath)
}

func TestShelleyProviderParsePhysicalAndVirtualSources(t *testing.T) {
	root, dbPath, db := newShelleyTestDB(t)
	seedShelleyMainConversation(t, db)
	seedShelleyConversation(
		t, db, "cAUX1", "Auxiliary", "/home/user/dev/aux",
		"claude-sonnet-4-6", "", true,
		"2026-06-15T11:00:00Z", "2026-06-15T11:03:00Z",
	)
	seedShelleyMessage(t, db, "cAUX1", 1, 1, "user",
		`{"Role":0,"Content":[{"Type":2,"Text":"Aux request"}]}`,
		"", "", "2026-06-15T11:00:00Z")

	provider, ok := NewProvider(AgentShelley, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)
	sources, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, sources, 1)

	allOutcome, err := provider.Parse(context.Background(), ParseRequest{
		Source: sources[0],
	})
	require.NoError(t, err)
	require.True(t, allOutcome.ResultSetComplete)
	require.True(t, allOutcome.ForceReplace)
	require.Len(t, allOutcome.Results, 2)
	assert.Equal(t, "shelley:cAUX1", allOutcome.Results[0].Result.Session.ID)
	assert.Equal(t, "shelley:cMAIN1", allOutcome.Results[1].Result.Session.ID)

	virtualSource, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		StoredFilePath: ShelleyVirtualPath(dbPath, "cMAIN1"),
	})
	require.NoError(t, err)
	require.True(t, ok)
	oneOutcome, err := provider.Parse(context.Background(), ParseRequest{
		Source: virtualSource,
	})
	require.NoError(t, err)
	require.True(t, oneOutcome.ResultSetComplete)
	require.True(t, oneOutcome.ForceReplace)
	require.Len(t, oneOutcome.Results, 1)
	assert.Equal(t, "shelley:cMAIN1", oneOutcome.Results[0].Result.Session.ID)
	assert.Equal(t, "devbox", oneOutcome.Results[0].Result.Session.Machine)
	assert.Len(t, oneOutcome.Results[0].Result.Messages, 5)
}

func TestShelleyProviderFingerprintChangesForSameSecondRewrite(t *testing.T) {
	root, _, db := newShelleyTestDB(t)
	seedShelleyMainConversation(t, db)

	provider, ok := NewProvider(AgentShelley, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		RawSessionID: "cMAIN1",
	})
	require.NoError(t, err)
	require.True(t, ok)

	before, err := provider.Fingerprint(context.Background(), source)
	require.NoError(t, err)

	_, err = db.Exec(
		`UPDATE messages
		    SET llm_data = ?
		  WHERE conversation_id = ? AND sequence_id = ?`,
		`{"Role":1,"Content":[{"Type":2,"Text":"Changed content."}]}`,
		"cMAIN1",
		4,
	)
	require.NoError(t, err)
	after, err := provider.Fingerprint(context.Background(), source)
	require.NoError(t, err)

	assert.Equal(t, before.MTimeNS, after.MTimeNS)
	assert.NotEqual(t, before.Hash, after.Hash)
}

func TestShelleyProviderFingerprintIncludesWALSiblings(t *testing.T) {
	root, dbPath, db := newShelleyTestDB(t)
	seedShelleyMainConversation(t, db)

	provider, ok := NewProvider(AgentShelley, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	before, err := provider.Fingerprint(context.Background(), sources[0])
	require.NoError(t, err)

	walPath := dbPath + "-wal"
	writeSourceFile(t, walPath, "wal")
	walTime := time.Unix(0, before.MTimeNS+int64(time.Second))
	require.NoError(t, os.Chtimes(walPath, walTime, walTime))
	after, err := provider.Fingerprint(context.Background(), sources[0])
	require.NoError(t, err)

	assert.Equal(t, before.Size, after.Size)
	assert.Greater(t, after.MTimeNS, before.MTimeNS)
}

func TestShelleyProviderClassifiesDeletedVirtualPath(t *testing.T) {
	root, dbPath, db := newShelleyTestDB(t)
	seedShelleyMainConversation(t, db)
	virtualPath := ShelleyVirtualPath(dbPath, "cMAIN1")
	require.NoError(t, os.Remove(dbPath))

	provider, ok := NewProvider(AgentShelley, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	changed, err := provider.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{Path: virtualPath, EventKind: "remove", WatchRoot: root},
	)
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, virtualPath, changed[0].DisplayPath)
}

func TestShelleyProviderClassifiesDeletedPhysicalDB(t *testing.T) {
	root, dbPath, db := newShelleyTestDB(t)
	seedShelleyMainConversation(t, db)
	require.NoError(t, os.Remove(dbPath))

	provider, ok := NewProvider(AgentShelley, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	changed, err := provider.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{Path: dbPath, EventKind: "remove", WatchRoot: root},
	)
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, dbPath, changed[0].DisplayPath)

	outcome, err := provider.Parse(context.Background(), ParseRequest{Source: changed[0]})
	require.NoError(t, err)
	assert.True(t, outcome.ResultSetComplete)
	assert.True(t, outcome.ForceReplace)
	assert.Equal(t, SkipNoSession, outcome.SkipReason)
	assert.Empty(t, outcome.Results)
}

func TestShelleyProviderIgnoresUnrelatedSidecarBasename(t *testing.T) {
	root := t.TempDir()
	provider, ok := NewProvider(AgentShelley, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)

	changed, err := provider.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{
			Path:      filepath.Join(root, "nested", shelleyDBName+"-wal"),
			EventKind: "remove",
			WatchRoot: filepath.Join(root, "nested"),
		},
	)
	require.NoError(t, err)
	assert.Empty(t, changed)
}
