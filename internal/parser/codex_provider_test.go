package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestCodexProviderFactoryReplacesLegacyAdapter(t *testing.T) {
	_, ok := ProviderFactoryByType(AgentCodex)
	require.True(t, ok)

	provider, ok := NewProvider(AgentCodex, ProviderConfig{
		Roots:   []string{t.TempDir()},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)
}

func TestCodexProviderSourceMethods(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "sessions")
	uuid := "019eb791-cf7d-75c1-8439-9ed74c1229e1"
	sourcePath := writeCodexProviderSession(t, root, uuid, "Rename me")
	indexPath := filepath.Join(base, CodexSessionIndexFilename)
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"id":"`+uuid+`","thread_name":"Renamed title","updated_at":"2026-06-11T17:34:20Z"}`+"\n",
	), 0o644))
	newer := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(indexPath, newer, newer))

	provider, ok := NewProvider(AgentCodex, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)

	plan, err := provider.WatchPlan(context.Background())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 2)
	assert.Equal(t, root, plan.Roots[0].Path)
	assert.True(t, plan.Roots[0].Recursive)
	assert.Equal(t, []string{"*.jsonl"}, plan.Roots[0].IncludeGlobs)
	assert.Equal(t, base, plan.Roots[1].Path)
	assert.False(t, plan.Roots[1].Recursive)
	assert.Equal(t, []string{CodexSessionIndexFilename}, plan.Roots[1].IncludeGlobs)

	discovered, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	source := discovered[0]
	assert.Equal(t, AgentCodex, source.Provider)
	assert.Equal(t, sourcePath, source.DisplayPath)
	assert.Equal(t, sourcePath, source.FingerprintKey)

	found, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		FullSessionID: "host~codex:" + uuid,
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, sourcePath, found.DisplayPath)

	for _, path := range []string{sourcePath, indexPath} {
		changed, err := provider.SourcesForChangedPath(
			context.Background(),
			ChangedPathRequest{Path: path, EventKind: "write"},
		)
		require.NoError(t, err)
		require.Len(t, changed, 1)
		assert.Equal(t, sourcePath, changed[0].DisplayPath)
	}

	info, err := os.Stat(sourcePath)
	require.NoError(t, err)
	fingerprint, err := provider.Fingerprint(context.Background(), found)
	require.NoError(t, err)
	assert.Equal(t, sourcePath, fingerprint.Key)
	assert.Equal(t, info.Size(), fingerprint.Size)
	assert.Equal(t, newer.UnixNano(), fingerprint.MTimeNS)
	assert.NotEmpty(t, fingerprint.Hash)

	outcome, err := provider.Parse(context.Background(), ParseRequest{
		Source:      found,
		Fingerprint: fingerprint,
	})
	require.NoError(t, err)
	require.True(t, outcome.ResultSetComplete)
	require.Len(t, outcome.Results, 1)
	result := outcome.Results[0]
	assert.Equal(t, DataVersionCurrent, result.DataVersion)
	assert.Equal(t, "codex:"+uuid, result.Result.Session.ID)
	assert.Equal(t, AgentCodex, result.Result.Session.Agent)
	assert.Equal(t, "devbox", result.Result.Session.Machine)
	assert.Equal(t, "api", result.Result.Session.Project)
	assert.Equal(t, "Renamed title", result.Result.Session.SessionName)
	assert.Equal(t, fingerprint.Hash, result.Result.Session.File.Hash)
	assert.Len(t, result.Result.Messages, 1)
}

func TestCodexProviderParseIncremental(t *testing.T) {
	root := t.TempDir()
	uuid := "019eb791-cf7d-75c1-8439-9ed74c1229e2"
	initial := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(uuid, "/tmp/api", "codex_cli_rs", tsEarly),
		testjsonl.CodexMsgJSON("user", "hello", tsEarlyS1),
	)
	sourcePath := writeCodexProviderSessionContent(t, root, uuid, initial)
	info, err := os.Stat(sourcePath)
	require.NoError(t, err)
	offset := info.Size()

	appendFile(t, sourcePath, testjsonl.JoinJSONL(
		testjsonl.CodexMsgJSON("assistant", "world", tsEarlyS5),
	))
	currentInfo, err := os.Stat(sourcePath)
	require.NoError(t, err)

	provider, ok := NewProvider(AgentCodex, ProviderConfig{
		Roots: []string{root},
	})
	require.True(t, ok)
	require.NotNil(t, provider)
	source, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		FullSessionID: "codex:" + uuid,
	})
	require.NoError(t, err)
	require.True(t, ok)

	outcome, status, err := provider.ParseIncremental(
		context.Background(),
		IncrementalRequest{
			Source:       source,
			Fingerprint:  SourceFingerprint{Size: currentInfo.Size()},
			SessionID:    "codex:" + uuid,
			Offset:       offset,
			StartOrdinal: 1,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, IncrementalApplied, status)
	assert.Equal(t, "codex:"+uuid, outcome.SessionID)
	assert.Equal(t, int64(len(testjsonl.JoinJSONL(
		testjsonl.CodexMsgJSON("assistant", "world", tsEarlyS5),
	))), outcome.ConsumedBytes)
	require.Len(t, outcome.Messages, 1)
	assert.Equal(t, RoleAssistant, outcome.Messages[0].Role)
	assert.Equal(t, "world", outcome.Messages[0].Content)
}

func TestCodexProviderParseIncrementalFallback(t *testing.T) {
	root := t.TempDir()
	uuid := "019eb791-cf7d-75c1-8439-9ed74c1229e3"
	initial := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(uuid, "/tmp/api", "codex_cli_rs", tsEarly),
		testjsonl.CodexTurnContextJSON("gpt-5.5", tsEarlyS1),
		testjsonl.CodexMsgJSON("user", "run command", tsEarlyS1),
		testjsonl.CodexFunctionCallWithCallIDJSON(
			"exec_command", "call_cmd",
			map[string]any{"cmd": "sleep 1"}, tsEarlyS5,
		),
	)
	sourcePath := writeCodexProviderSessionContent(t, root, uuid, initial)
	info, err := os.Stat(sourcePath)
	require.NoError(t, err)
	offset := info.Size()
	appendFile(t, sourcePath, testjsonl.JoinJSONL(
		testjsonl.CodexFunctionCallOutputJSON("call_cmd", "done", tsLate),
		testjsonl.CodexTokenCountJSON(tsLate, 100_000, 250, 64_000),
	))
	currentInfo, err := os.Stat(sourcePath)
	require.NoError(t, err)

	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		FullSessionID: "codex:" + uuid,
	})
	require.NoError(t, err)
	require.True(t, ok)

	outcome, status, err := provider.ParseIncremental(
		context.Background(),
		IncrementalRequest{
			Source:       source,
			Fingerprint:  SourceFingerprint{Size: currentInfo.Size()},
			SessionID:    "codex:" + uuid,
			Offset:       offset,
			StartOrdinal: 2,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, IncrementalNeedsFullParse, status)
	assert.True(t, outcome.ForceReplace)
}

func writeCodexProviderSession(
	t *testing.T,
	root, uuid, prompt string,
) string {
	t.Helper()
	content := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(uuid, "/home/user/code/api", "codex_cli_rs", tsEarly),
		testjsonl.CodexMsgJSON("user", prompt, tsEarlyS1),
	)
	return writeCodexProviderSessionContent(t, root, uuid, content)
}

func writeCodexProviderSessionContent(
	t *testing.T,
	root, uuid, content string,
) string {
	t.Helper()
	path := filepath.Join(
		root,
		"2026",
		"06",
		"11",
		"rollout-2026-06-11T12-44-06-"+uuid+".jsonl",
	)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}
