package transcript_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// impure reports whether a package may not be imported here: a clock, the
// operating system, the network, or anything that draws.
func impure(path string) bool {
	for _, banned := range []string{"time", "os", "net", "charm.land"} {
		if path == banned || strings.HasPrefix(path, banned+"/") {
			return true
		}
	}
	return false
}

// The package is a reducer over values. It reads no clock, file or socket
// and draws nothing, which is what lets every behaviour be tested by feeding
// it values.
func TestThePackageStaysPure(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if impure(path) {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
	if checked == 0 {
		t.Error("found no source file to check")
	}
}

func TestImpure(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"time": true, "os": true, "os/exec": true, "net": true, "net/http": true,
		"charm.land/lipgloss/v2": true, "charm.land/bubbletea/v2": true,
		"slices": false, "strings": false, "encoding/json": false, "network": false, "timex/time": false,
		"github.com/quinnovator/contactd/internal/core": false,
	} {
		if got := impure(path); got != want {
			t.Errorf("impure(%q) = %t, want %t", path, got, want)
		}
	}
}
