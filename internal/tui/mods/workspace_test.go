package mods

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
)

// write makes a file and the directories above it.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mkdir makes a directory and returns it.
func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadWorkspace(t *testing.T) {
	t.Parallel()
	const detached = "4b825dc642cb6eb9a060e54bf8d69288fbee4904\n"
	cases := []struct {
		name string
		// setup builds the tree under root and returns the directory the
		// client starts in.
		setup func(t *testing.T, root string) string
		want  wire.Workspace
	}{
		{
			name: "a plain repository",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "contactd", ".git", "HEAD"), "ref: refs/heads/main\n")
				return filepath.Join(root, "contactd")
			},
			want: wire.Workspace{Dir: "contactd", Branch: "main"},
		},
		{
			name: "a directory deep inside one",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "repo", ".git", "HEAD"), "ref: refs/heads/tui-frame\n")
				return mkdir(t, filepath.Join(root, "repo", "internal", "tui", "mods"))
			},
			want: wire.Workspace{Dir: "mods", Branch: "tui-frame"},
		},
		{
			name: "a branch with a slash in its name",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "repo", ".git", "HEAD"), "ref: refs/heads/feature/mods-host\n")
				return filepath.Join(root, "repo")
			},
			want: wire.Workspace{Dir: "repo", Branch: "feature/mods-host"},
		},
		{
			name: "a worktree, whose .git is a file",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				main := filepath.Join(root, "main", ".git")
				write(t, filepath.Join(main, "HEAD"), "ref: refs/heads/main\n")
				write(t, filepath.Join(main, "worktrees", "fix", "HEAD"), "ref: refs/heads/fix-123\n")
				write(t, filepath.Join(root, "fix", ".git"), "gitdir: "+filepath.Join(main, "worktrees", "fix")+"\n")
				return filepath.Join(root, "fix")
			},
			want: wire.Workspace{Dir: "fix", Branch: "fix-123"},
		},
		{
			name: "a submodule, whose .git file is relative",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "outer", ".git", "HEAD"), "ref: refs/heads/outer\n")
				write(t, filepath.Join(root, "outer", ".git", "modules", "inner", "HEAD"), "ref: refs/heads/inner\n")
				write(t, filepath.Join(root, "outer", "inner", ".git"), "gitdir: ../.git/modules/inner\n")
				return mkdir(t, filepath.Join(root, "outer", "inner", "src"))
			},
			want: wire.Workspace{Dir: "src", Branch: "inner"},
		},
		{
			name: "a detached head",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "repo", ".git", "HEAD"), detached)
				return filepath.Join(root, "repo")
			},
			want: wire.Workspace{Dir: "repo"},
		},
		{
			name: "a detached head in a worktree",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "main", ".git", "worktrees", "wt", "HEAD"), detached)
				write(t, filepath.Join(root, "wt", ".git"), "gitdir: "+filepath.Join(root, "main", ".git", "worktrees", "wt")+"\n")
				return filepath.Join(root, "wt")
			},
			want: wire.Workspace{Dir: "wt"},
		},
		{
			name: "no repository",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				return mkdir(t, filepath.Join(root, "notes"))
			},
			want: wire.Workspace{Dir: "notes"},
		},
		{
			name: "a ref that is not a branch",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "repo", ".git", "HEAD"), "ref: refs/remotes/origin/main\n")
				return filepath.Join(root, "repo")
			},
			want: wire.Workspace{Dir: "repo"},
		},
		{
			name: "a .git file that names nothing",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "repo", ".git"), "gitdir: /nowhere/at/all\n")
				return filepath.Join(root, "repo")
			},
			want: wire.Workspace{Dir: "repo"},
		},
		{
			name: "a .git file that is something else",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "repo", ".git"), "not a gitdir line\n")
				return filepath.Join(root, "repo")
			},
			want: wire.Workspace{Dir: "repo"},
		},
		{
			name: "a HEAD that is a directory",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				mkdir(t, filepath.Join(root, "repo", ".git", "HEAD"))
				return filepath.Join(root, "repo")
			},
			want: wire.Workspace{Dir: "repo"},
		},
		{
			name: "a HEAD too large to be one",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "repo", ".git", "HEAD"), "ref: refs/heads/"+strings.Repeat("x", 5000)+"\n")
				return filepath.Join(root, "repo")
			},
			want: wire.Workspace{Dir: "repo"},
		},
		{
			name: "a branch name with an escape sequence in it",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "repo", ".git", "HEAD"), "ref: refs/heads/x\x1b]0;owned\x07y\n")
				return filepath.Join(root, "repo")
			},
			want: wire.Workspace{Dir: "repo", Branch: "x]0;ownedy"},
		},
		{
			name: "the nearest repository wins",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, "outer", ".git", "HEAD"), "ref: refs/heads/outer\n")
				write(t, filepath.Join(root, "outer", "vendor", "dep", ".git", "HEAD"), "ref: refs/heads/dep\n")
				return filepath.Join(root, "outer", "vendor", "dep")
			},
			want: wire.Workspace{Dir: "dep", Branch: "dep"},
		},
		{
			name: "a repository more than 32 levels up is not looked for",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/far\n")
				deep := root
				for range 32 {
					deep = filepath.Join(deep, "d")
				}
				return mkdir(t, deep)
			},
			want: wire.Workspace{Dir: "d"},
		},
		{
			name: "a repository 31 levels up is found",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/near\n")
				deep := root
				for range 31 {
					deep = filepath.Join(deep, "d")
				}
				return mkdir(t, deep)
			},
			want: wire.Workspace{Dir: "d", Branch: "near"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The root is fenced with a .git that names no branch, so a
			// repository that happens to hold the temporary directory is
			// never reached.
			fence := t.TempDir()
			write(t, filepath.Join(fence, ".git", "HEAD"), detached)
			dir := tc.setup(t, mkdir(t, filepath.Join(fence, "root")))
			if got := ReadWorkspace(dir); got != tc.want {
				t.Errorf("ReadWorkspace = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadWorkspaceOfARelativePath(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// The test runs in this package's directory, inside the repository.
	got := ReadWorkspace(".")
	if got.Dir != filepath.Base(wd) {
		t.Errorf("Dir = %q, want %q", got.Dir, filepath.Base(wd))
	}
	if strings.ContainsAny(got.Dir+got.Branch, "\x1b\n") || strings.Contains(got.Dir, string(filepath.Separator)) {
		t.Errorf("ReadWorkspace(\".\") = %+v", got)
	}
}
