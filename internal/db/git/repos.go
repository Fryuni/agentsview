// Package git discovers local repositories and aggregates git-derived metrics
// for session analytics.
package git

import (
	"os"
	"path/filepath"
)

// DiscoverRepos walks up from each cwd looking for a `.git` directory and
// returns a deduplicated list of repo toplevels that exist.
//
// Cwds with no enclosing repo are silently dropped. Order in the result
// follows first-seen order in the input.
//
// Limitation: only directories named `.git` are recognized. Git worktrees and
// submodules represent `.git` as a FILE containing `gitdir: <path>`; such cwds
// are not treated as repo roots by this function. Callers that need worktree
// support should resolve the gitdir themselves or shell out to
// `git rev-parse --show-toplevel`.
func DiscoverRepos(cwds []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, cwd := range cwds {
		root := findRepoRoot(cwd)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

// findRepoRoot walks upward from start until it finds a directory containing a
// `.git` subdirectory, or returns "" if the filesystem root is reached.
func findRepoRoot(start string) string {
	dir := start
	for {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
