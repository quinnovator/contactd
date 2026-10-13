// Package tuitest holds what the terminal client's tests share: golden
// files, and a recorder for the bytes a program writes to its terminal.
package tuitest

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// updateFlag is the test flag that rewrites golden files. It is looked up by
// name where it is needed, because the linter forbids a package variable.
const updateFlag = "update"

// Main registers the -update flag and runs the tests. Every package with
// goldens has: func TestMain(m *testing.M) { tuitest.Main(m) }
func Main(m *testing.M) {
	register()
	os.Exit(m.Run())
}

// register adds the -update flag unless the test binary already has one.
// Another golden-file package linked into the same binary may have defined
// it, and defining a flag twice panics. Whoever defined it, updating reads it.
func register() {
	if flag.Lookup(updateFlag) == nil {
		flag.Bool(updateFlag, false, "rewrite golden files")
	}
}

// Golden compares got with testdata/<name>, or rewrites it under -update.
// A difference fails the test and does not stop it. The name uses forward
// slashes and stays inside testdata.
func Golden(t *testing.T, name string, got []byte) {
	t.Helper()
	if problem := compare("testdata", name, got, updating()); problem != "" {
		t.Error(problem)
	}
}

// updating reports whether the tests were run with -update.
func updating() bool {
	f := flag.Lookup(updateFlag)
	return f != nil && f.Value.String() == "true"
}

// compare does the work of Golden under a directory of the caller's choice.
// It returns what is wrong, or "".
func compare(dir, name string, got []byte, update bool) string {
	if !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Sprintf("golden name %q does not stay inside %s", name, dir)
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	if update {
		// A golden file is checked in with the source and read by everyone
		// who reads the repository.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: a directory of test data in the repository
			return fmt.Sprintf("write golden: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil { //nolint:gosec // G306: a file of test data in the repository
			return fmt.Sprintf("write golden: %v", err)
		}
		return ""
	}
	want, err := os.ReadFile(path) //nolint:gosec // G304: the path is under the test's own testdata directory
	if err != nil {
		return fmt.Sprintf("read golden: %v (run the test with -update to create it)", err)
	}
	if bytes.Equal(got, want) {
		return ""
	}
	return fmt.Sprintf("%s differs (run the test with -update to accept it)\n%s", path, firstDifference(got, want))
}

// firstDifference describes the first line on which two texts differ. Lines
// are quoted, so that escape sequences and trailing spaces can be read.
func firstDifference(got, want []byte) string {
	g, w := bytes.SplitAfter(got, []byte("\n")), bytes.SplitAfter(want, []byte("\n"))
	n := 0
	for n < len(g) && n < len(w) && bytes.Equal(g[n], w[n]) {
		n++
	}
	return fmt.Sprintf("line %d of %d (want %d):\n got: %s\nwant: %s",
		n+1, lines(g), lines(w), quoted(g, n), quoted(w, n))
}

// lines counts the lines of a text split after each newline: a text that
// ends in a newline has an empty last piece, which is not a line.
func lines(parts [][]byte) int {
	if n := len(parts); n > 0 && len(parts[n-1]) == 0 {
		return n - 1
	}
	return len(parts)
}

// quoted is line n of a text, quoted, or a note that there is no such line.
func quoted(parts [][]byte, n int) string {
	if n >= lines(parts) {
		return "(no such line)"
	}
	return fmt.Sprintf("%q", parts[n])
}
