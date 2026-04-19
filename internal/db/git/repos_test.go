package git

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// mkdir creates dir (and parents) under root and returns the absolute path.
func mkdir(t *testing.T, root string, rel string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

func TestDiscoverRepos_FindsRootAndFiltersMissing(t *testing.T) {
	root := t.TempDir()
	repoA := mkdir(t, root, "repoA")
	mkdir(t, root, "repoA/.git")
	sub := mkdir(t, root, "repoA/subdir")
	outside := mkdir(t, root, "outside")

	got := DiscoverRepos([]string{sub, outside})
	want := []string{repoA}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverRepos = %v, want %v", got, want)
	}
}

func TestDiscoverRepos_Dedup(t *testing.T) {
	root := t.TempDir()
	repoA := mkdir(t, root, "repoA")
	mkdir(t, root, "repoA/.git")
	sub1 := mkdir(t, root, "repoA/sub1")
	sub2 := mkdir(t, root, "repoA/sub2/deeper")

	got := DiscoverRepos([]string{sub1, sub2, repoA})
	want := []string{repoA}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverRepos = %v, want %v (expected dedup to single repo)", got, want)
	}
}

func TestDiscoverRepos_EmptyInputReturnsEmptySlice(t *testing.T) {
	got := DiscoverRepos(nil)
	if got == nil {
		t.Fatalf("DiscoverRepos(nil) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("DiscoverRepos(nil) = %v, want empty slice", got)
	}

	got = DiscoverRepos([]string{})
	if got == nil {
		t.Fatalf("DiscoverRepos([]) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("DiscoverRepos([]) = %v, want empty slice", got)
	}
}

func TestDiscoverRepos_GitFileNotDirectoryIsIgnored(t *testing.T) {
	// Modern git worktrees/submodules use `.git` as a FILE rather than a directory.
	// The documented contract says we only recognize DIRECTORIES, so such a cwd
	// should walk past it (and, with no real repo above, be filtered out).
	root := t.TempDir()
	wtree := mkdir(t, root, "worktree")
	gitFile := filepath.Join(wtree, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", gitFile, err)
	}

	got := DiscoverRepos([]string{wtree})
	if len(got) != 0 {
		t.Fatalf("DiscoverRepos = %v, want empty (`.git` file, not directory)", got)
	}
}
