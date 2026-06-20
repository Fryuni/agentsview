package parser

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/testjsonl"
)

func TestGeminiCopilotProviderFactoriesReplaceLegacyAdapter(t *testing.T) {
	for _, agent := range []AgentType{AgentGemini, AgentCopilot} {
		t.Run(string(agent), func(t *testing.T) {
			_, ok := ProviderFactoryByType(agent)
			require.True(t, ok)

			provider, ok := NewProvider(agent, ProviderConfig{
				Roots:   []string{t.TempDir()},
				Machine: "devbox",
			})
			require.True(t, ok)
			require.NotNil(t, provider)
		})
	}
}

func TestGeminiProviderSourceMethods(t *testing.T) {
	root := t.TempDir()
	sessionID := "gemini-provider"
	sourcePath := filepath.Join(
		root,
		"tmp",
		"my-project",
		geminiChatsDir,
		"session-2026-06-19T12-00-gemini-provider.json",
	)
	writeSourceFile(t, sourcePath, testjsonl.GeminiSessionJSON(
		sessionID,
		"my-project",
		tsEarly,
		tsEarlyS5,
		[]map[string]any{
			testjsonl.GeminiUserMsg("u1", tsEarly, "hello gemini"),
			testjsonl.GeminiAssistantMsg("a1", tsEarlyS5, "hi", nil),
		},
	))

	provider, ok := NewProvider(AgentGemini, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)

	plan, err := provider.WatchPlan(context.Background())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 1)
	assert.Equal(t, filepath.Join(root, "tmp"), plan.Roots[0].Path)
	assert.True(t, plan.Roots[0].Recursive)
	assert.Equal(t, []string{"session-*.json", "session-*.jsonl"}, plan.Roots[0].IncludeGlobs)

	discovered, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	assert.Equal(t, sourcePath, discovered[0].DisplayPath)
	assert.Equal(t, "my_project", discovered[0].ProjectHint)

	found, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		FullSessionID: "host~gemini:" + sessionID,
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, sourcePath, found.DisplayPath)

	changed, err := provider.SourcesForChangedPath(
		context.Background(),
		ChangedPathRequest{Path: sourcePath, EventKind: "write", WatchRoot: filepath.Join(root, "tmp")},
	)
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, sourcePath, changed[0].DisplayPath)

	fingerprint, err := provider.Fingerprint(context.Background(), found)
	require.NoError(t, err)
	assert.Equal(t, sourcePath, fingerprint.Key)
	assert.Positive(t, fingerprint.Size)
	assert.Positive(t, fingerprint.MTimeNS)
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
	assert.Equal(t, "gemini:"+sessionID, result.Result.Session.ID)
	assert.Equal(t, AgentGemini, result.Result.Session.Agent)
	assert.Equal(t, "my_project", result.Result.Session.Project)
	assert.Equal(t, "devbox", result.Result.Session.Machine)
	assert.Equal(t, fingerprint.Hash, result.Result.Session.File.Hash)
	assert.Len(t, result.Result.Messages, 2)
}

func TestCopilotProviderSourceMethods(t *testing.T) {
	root := t.TempDir()
	barePath := filepath.Join(root, copilotStateDir, "copilot-provider.jsonl")
	dirEvents := filepath.Join(root, copilotStateDir, "copilot-provider", "events.jsonl")
	workspacePath := filepath.Join(root, copilotStateDir, "copilot-provider", "workspace.yaml")
	content := strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"copilot-provider","context":{"cwd":"/home/user/code/copilot-app","branch":"main"}},"timestamp":"2025-01-15T10:00:00Z"}`,
		`{"type":"user.message","data":{"content":"hello copilot"},"timestamp":"2025-01-15T10:00:01Z"}`,
		`{"type":"assistant.message","data":{"content":"hi"},"timestamp":"2025-01-15T10:00:02Z"}`,
	}, "\n") + "\n"
	writeSourceFile(t, barePath, content)
	writeSourceFile(t, dirEvents, content)
	writeSourceFile(t, workspacePath, "name: Workspace title\n")

	provider, ok := NewProvider(AgentCopilot, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	require.NotNil(t, provider)

	plan, err := provider.WatchPlan(context.Background())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 1)
	assert.Equal(t, filepath.Join(root, copilotStateDir), plan.Roots[0].Path)
	assert.True(t, plan.Roots[0].Recursive)
	assert.Equal(t, []string{"*.jsonl", "workspace.yaml"}, plan.Roots[0].IncludeGlobs)

	discovered, err := provider.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	assert.Equal(t, dirEvents, discovered[0].DisplayPath)

	found, ok, err := provider.FindSource(context.Background(), FindSourceRequest{
		RawSessionID: "copilot-provider",
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, dirEvents, found.DisplayPath)

	for _, path := range []string{dirEvents, workspacePath} {
		changed, err := provider.SourcesForChangedPath(
			context.Background(),
			ChangedPathRequest{Path: path, EventKind: "write", WatchRoot: filepath.Join(root, copilotStateDir)},
		)
		require.NoError(t, err)
		require.Len(t, changed, 1)
		assert.Equal(t, dirEvents, changed[0].DisplayPath)
	}

	fingerprint, err := provider.Fingerprint(context.Background(), found)
	require.NoError(t, err)
	assert.Equal(t, dirEvents, fingerprint.Key)
	assert.Positive(t, fingerprint.Size)
	assert.Positive(t, fingerprint.MTimeNS)
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
	assert.Equal(t, "copilot:copilot-provider", result.Result.Session.ID)
	assert.Equal(t, AgentCopilot, result.Result.Session.Agent)
	assert.Equal(t, "copilot_app", result.Result.Session.Project)
	assert.Equal(t, "Workspace title", result.Result.Session.FirstMessage)
	assert.Equal(t, "devbox", result.Result.Session.Machine)
	assert.Equal(t, fingerprint.Hash, result.Result.Session.File.Hash)
	assert.Len(t, result.Result.Messages, 2)
}
