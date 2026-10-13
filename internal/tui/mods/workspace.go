package mods

import (
	"path/filepath"
	"strings"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/safe"
)

// The bounds of ReadWorkspace.
const (
	maxGitLevels = 32      // directories searched upward for .git
	maxGitFile   = 4 << 10 // of HEAD, and of a .git file
	maxNameRunes = 128     // of a directory name or a branch
)

// ReadWorkspace describes where the client started, for mods that hold the
// workspace grant: the base name of dir and the git branch checked out
// there. A mod has no files, so the host reads the branch for it.
//
// It finds .git upward from dir (at most 32 levels) and reads HEAD. A .git
// that is a file, as in a linked worktree or a submodule, is followed to the
// directory it names. The branch is empty when there is no repository, when
// HEAD is detached, and when anything cannot be read. It runs no command.
func ReadWorkspace(dir string) wire.Workspace {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return wire.Workspace{}
	}
	ws := wire.Workspace{Dir: safe.Line(filepath.Base(abs), maxNameRunes, false)}
	if head, ok := findHead(abs); ok {
		ws.Branch = branchOf(head)
	}
	return ws
}

// findHead returns the contents of HEAD for the repository that holds dir.
func findHead(dir string) (string, bool) {
	for range maxGitLevels {
		if head, ok := headIn(filepath.Join(dir, ".git")); ok {
			return head, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// headIn reads HEAD given a .git path: a directory that holds it, or a file
// whose "gitdir:" line names the directory that does. A relative gitdir is
// relative to the file's own directory.
func headIn(dotGit string) (string, bool) {
	if head, err := readRegular(filepath.Join(dotGit, "HEAD"), maxGitFile); err == nil {
		return string(head), true
	}
	link, err := readRegular(dotGit, maxGitFile)
	if err != nil {
		return "", false
	}
	first, _, _ := strings.Cut(string(link), "\n")
	gitDir, ok := strings.CutPrefix(first, "gitdir:")
	if !ok {
		return "", false
	}
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return "", false
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(dotGit), gitDir)
	}
	head, err := readRegular(filepath.Join(gitDir, "HEAD"), maxGitFile)
	if err != nil {
		return "", false
	}
	return string(head), true
}

// branchOf returns the branch a HEAD names: what follows "ref: refs/heads/".
// A detached HEAD holds a commit id and names no branch.
func branchOf(head string) string {
	first, _, _ := strings.Cut(head, "\n")
	ref, ok := strings.CutPrefix(strings.TrimSpace(first), "ref:")
	if !ok {
		return ""
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/")
	if !ok {
		return ""
	}
	return safe.Line(branch, maxNameRunes, false)
}
