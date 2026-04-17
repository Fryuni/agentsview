// ABOUTME: CLI subcommand that returns token usage data for a
// ABOUTME: session, syncing on-demand if no server is running.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wesm/agentsview/internal/config"
	"github.com/wesm/agentsview/internal/db"
	"github.com/wesm/agentsview/internal/parser"
	"github.com/wesm/agentsview/internal/server"
	"github.com/wesm/agentsview/internal/sync"
)

// Exit codes for the token-use subcommand.
const (
	tokenUseExitOK            = 0
	tokenUseExitErr           = 1
	tokenUseExitNotFound      = 2
	tokenUseExitNoTokenData   = 3
	tokenUseResolveMatchLimit = 2
)

// isCanonicalSessionID reports whether id is already in the
// canonical form stored in sessions.id: either a host-prefixed
// remote ID ("host~<id>") or an ID beginning with a registered
// agent prefix ("codex:", "kimi:", ...). A bare input with no
// recognised prefix is treated as a raw ID even when it contains
// ':' because agents like Kimi and OpenClaw emit colon-bearing
// raw IDs.
func isCanonicalSessionID(id string) bool {
	host, rawID := parser.StripHostPrefix(id)
	if host != "" {
		return true
	}
	for _, def := range parser.Registry {
		if def.IDPrefix != "" &&
			strings.HasPrefix(rawID, def.IDPrefix) {
			return true
		}
	}
	return false
}

// resolveRawSessionID translates a user-supplied session ID into
// the canonical form stored in sessions.id. Callers may pass
// either a canonical ID ("codex:<uuid>") or a bare raw ID as
// emitted by the underlying agent — including raw IDs that
// contain colons themselves (Kimi: "<project-hash>:<session-uuid>",
// OpenClaw: "<agentId>:<sessionId>"). Resolution order:
//
//  1. Input already carries a canonical prefix (host~... or
//     <agent>:...) -> returned unchanged.
//  2. DB lookup: exact match OR suffix match against ":<input>";
//     exact match wins, otherwise most-recent suffix match wins
//     (rare ambiguity is reported to stderr).
//  3. Disk probe: iterate file-based agents and check their
//     FindSourceFunc for any configured directory; first hit
//     yields "<prefix><input>".
//  4. No match anywhere: returned unchanged with known=false.
//
// known reports whether resolution found evidence for the ID.
// When false, the caller should skip on-demand sync because it
// cannot produce meaningful output.
func resolveRawSessionID(
	ctx context.Context,
	database *db.DB,
	agentDirs map[parser.AgentType][]string,
	input string,
) (resolved string, known bool) {
	if isCanonicalSessionID(input) {
		return input, true
	}

	matches, err := database.FindSessionIDsByRawSuffix(
		ctx, input, tokenUseResolveMatchLimit,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: session id lookup failed: %v\n", err)
	}
	if len(matches) > 0 {
		for _, m := range matches {
			if m == input {
				return m, true
			}
		}
		if len(matches) > 1 {
			fmt.Fprintf(os.Stderr,
				"warning: ambiguous session id %q matches "+
					"multiple sessions, using most recent (%s)\n",
				input, matches[0],
			)
		}
		return matches[0], true
	}

	for _, def := range parser.Registry {
		if !def.FileBased || def.FindSourceFunc == nil {
			continue
		}
		for _, dir := range agentDirs[def.Type] {
			if def.FindSourceFunc(dir, input) != "" {
				return def.IDPrefix + input, true
			}
		}
	}

	return input, false
}

// tokenUseExitCode classifies a session record into an exit code:
// 0 when token metrics are present, 2 when the session is not in
// the DB, and 3 when the session exists but has no token data
// yet (e.g. the parser hasn't ingested it or the agent never
// emitted usage metadata).
func tokenUseExitCode(sess *db.Session) int {
	if sess == nil {
		return tokenUseExitNotFound
	}
	if sess.HasTotalOutputTokens || sess.HasPeakContextTokens {
		return tokenUseExitOK
	}
	return tokenUseExitNoTokenData
}

// tokenUseOutput is the JSON structure written to stdout.
// This format is experimental and may change.
type tokenUseOutput struct {
	SessionID         string `json:"session_id"`
	Agent             string `json:"agent"`
	Project           string `json:"project"`
	TotalOutputTokens int    `json:"total_output_tokens"`
	PeakContextTokens int    `json:"peak_context_tokens"`
	HasTokenData      bool   `json:"has_token_data"`
	ServerRunning     bool   `json:"server_running"`
}

// startupWaitTimeout is how long token-use will wait for a
// starting server to become ready before falling back to
// on-demand sync.
const startupWaitTimeout = 30 * time.Second

func runTokenUse(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr,
			"usage: agentsview token-use <session-id>")
		os.Exit(tokenUseExitErr)
	}

	code, err := tokenUse(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(tokenUseExitErr)
	}
	os.Exit(code)
}

func tokenUse(sessionID string) (int, error) {
	appCfg, err := config.LoadMinimal()
	if err != nil {
		return tokenUseExitErr, fmt.Errorf("loading config: %w", err)
	}

	if err := os.MkdirAll(appCfg.DataDir, 0o755); err != nil {
		return tokenUseExitErr,
			fmt.Errorf("creating data dir: %w", err)
	}

	serverActive := server.IsServerActive(appCfg.DataDir)

	// If a server is actively starting up (startup lock
	// present), wait for it to finish so we read fresh data
	// rather than returning stale results or "not found".
	// We only wait when the startup lock is the reason
	// IsServerActive returned true — if a state file has a
	// live PID but the TCP probe is transiently failing,
	// the server is running and we should just read the DB.
	if serverActive &&
		server.FindRunningServer(appCfg.DataDir) == nil {
		if server.IsStartupLocked(appCfg.DataDir) {
			fmt.Fprintf(os.Stderr,
				"server is starting up, waiting...\n")
			if !server.WaitForStartup(
				appCfg.DataDir, startupWaitTimeout,
			) {
				if server.IsStartupLocked(appCfg.DataDir) {
					// Lock still live after timeout:
					// the server is active (still
					// syncing, or state file write
					// failed). Don't compete — read
					// the DB as-is.
					fmt.Fprintf(os.Stderr,
						"server still starting after "+
							"%s, reading DB as-is\n",
						startupWaitTimeout,
					)
				} else {
					// Lock cleared but no running
					// server. Re-check in case of
					// transient TCP failure.
					serverActive = server.IsServerActive(
						appCfg.DataDir,
					)
				}
			}
		} else if !server.IsServerActive(appCfg.DataDir) {
			// The server that was alive at the first check
			// has since exited. Fall back to on-demand sync.
			serverActive = false
		}
	}

	database, err := db.Open(appCfg.DBPath)
	if err != nil {
		return tokenUseExitErr,
			fmt.Errorf("opening database: %w", err)
	}
	defer database.Close()

	if appCfg.CursorSecret != "" {
		secret, decErr := base64.StdEncoding.DecodeString(
			appCfg.CursorSecret,
		)
		if decErr != nil {
			return tokenUseExitErr, fmt.Errorf(
				"invalid cursor secret: %w", decErr,
			)
		}
		database.SetCursorSecret(secret)
	}

	ctx := context.Background()
	resolvedID, known := resolveRawSessionID(
		ctx, database, appCfg.AgentDirs, sessionID,
	)

	// If no server is managing the DB, do an on-demand sync
	// for this session so the data is fresh. Re-check right
	// before syncing to close the TOCTOU window where a
	// server could have started since our initial probe.
	// If the re-check detects a starting server, wait for
	// it rather than reading potentially stale data.
	if !serverActive {
		serverActive = server.IsServerActive(appCfg.DataDir)
		if serverActive &&
			server.FindRunningServer(appCfg.DataDir) == nil &&
			server.IsStartupLocked(appCfg.DataDir) {
			fmt.Fprintf(os.Stderr,
				"server is starting up, waiting...\n")
			if server.WaitForStartup(
				appCfg.DataDir, startupWaitTimeout,
			) {
				// Server is ready; read DB below.
			} else if !server.IsStartupLocked(
				appCfg.DataDir,
			) {
				// Lock cleared, no running server
				// via TCP. Re-check: a live state
				// file (transient probe failure)
				// still means the server is active.
				serverActive = server.IsServerActive(
					appCfg.DataDir,
				)
			}
			// Lock still live after timeout: server is
			// active but slow. Read DB as-is.
		}
	}
	// Skip sync entirely when we have no evidence of the
	// session (known=false) — SyncSingleSession would just
	// log a misleading "source file not found" warning.
	if !serverActive && known {
		engine := sync.NewEngine(database, sync.EngineConfig{
			AgentDirs:               appCfg.AgentDirs,
			Machine:                 "local",
			BlockedResultCategories: appCfg.ResultContentBlockedCategories,
		})
		if syncErr := engine.SyncSingleSession(
			resolvedID,
		); syncErr != nil {
			// Not fatal: session may already be in the DB
			// from a previous sync, or may not exist at all.
			fmt.Fprintf(os.Stderr,
				"warning: sync failed: %v\n", syncErr)
		}
	}

	sess, err := database.GetSession(ctx, resolvedID)
	if err != nil {
		return tokenUseExitErr,
			fmt.Errorf("querying session: %w", err)
	}
	if sess == nil {
		fmt.Fprintf(os.Stderr,
			"session not found: %s\n", sessionID)
		return tokenUseExitNotFound, nil
	}

	agent := sess.Agent
	if agent == "" {
		if def, ok := parser.AgentByPrefix(sess.ID); ok {
			agent = string(def.Type)
		}
	}

	out := tokenUseOutput{
		SessionID:         sess.ID,
		Agent:             agent,
		Project:           sess.Project,
		TotalOutputTokens: sess.TotalOutputTokens,
		PeakContextTokens: sess.PeakContextTokens,
		HasTokenData: sess.HasTotalOutputTokens ||
			sess.HasPeakContextTokens,
		ServerRunning: serverActive,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return tokenUseExitErr, err
	}
	return tokenUseExitCode(sess), nil
}
