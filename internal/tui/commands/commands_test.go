package commands_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/commands"
)

// names lists the names of some commands, in order.
func names(specs []commands.Spec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// find returns the core command with a name.
func find(t *testing.T, name string) commands.Spec {
	t.Helper()
	for _, s := range commands.Core() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no core command %q", name)
	return commands.Spec{}
}

// The core commands are the spec's table: these names, in this order, with
// this usage and key. /statusline is the one exception: its usage is the
// short form the Commands mock draws as ghost text.
func TestCoreIsTheDeclaredList(t *testing.T) {
	t.Parallel()
	type row struct{ name, usage, key string }
	want := []row{
		{"statusline", "preset | edit", ""},
		{"status", "", ""},
		{"stop", "", "esc"},
		{"help", "[command]", ""},
		{"contacts", "", "ctrl+p"},
		{"open", "<contact>", ""},
		{"new", "", ""},
		{"rename", "[name]", ""},
		{"details", "", "ctrl+o"},
		{"mods", "[on|off|install|update <x>]", ""},
		{"image", "pixels | blocks | text | auto | save <n> [path]", ""},
		{"avatar", "on | off | auto | look <name> | voice <name> | rate <n> | cells <kind> | mute | unmute", ""},
		{"theme", "auto | dark | light", ""},
		{"glyphs", "auto | unicode | ascii", ""},
		{"color", "auto | truecolor | 256 | 16 | none", ""},
		{"top", "", ""},
		{"bottom", "", ""},
		{"redraw", "", "ctrl+l"},
		{"reload", "", ""},
		{"quit", "", "ctrl+c"},
		{"helpers", "", ""},
		{"a2a", "", ""},
		{"helper", "<n>", ""},
	}
	core := commands.Core()
	got := make([]row, 0, len(core))
	for _, s := range core {
		got = append(got, row{s.Name, s.Usage, s.Key})
	}
	if !slices.Equal(got, want) {
		t.Errorf("core commands:\n%v\nwant:\n%v", got, want)
	}
}

func TestCoreHasNoDuplicateNames(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for _, s := range commands.Core() {
		if seen[s.Name] {
			t.Errorf("/%s is declared twice", s.Name)
		}
		seen[s.Name] = true
	}
}

func TestCoreSpecsAreWellFormed(t *testing.T) {
	t.Parallel()
	core := commands.Core()
	for _, s := range core {
		if s.Source != "core" {
			t.Errorf("/%s has source %q, want core", s.Name, s.Source)
		}
		if s.Name != strings.ToLower(s.Name) {
			t.Errorf("/%s is not in lower case", s.Name)
		}
		if s.Summary == "" || strings.ContainsAny(s.Summary, "\n\t") {
			t.Errorf("/%s has summary %q, want one line", s.Name, s.Summary)
		}
		if got := s.NeedsArg; got != (s.Name == "open" || s.Name == "helper") {
			t.Errorf("/%s has NeedsArg %t; only /open needs an argument", s.Name, got)
		}
		// The Commands mock draws the key at the end of the summary.
		if got, want := strings.HasSuffix(s.Summary, ")"), s.Key != ""; got != want {
			t.Errorf("/%s: summary %q with key %q", s.Name, s.Summary, s.Key)
		}
		if s.Key != "" && !strings.HasSuffix(s.Summary, " ("+s.Key+")") {
			t.Errorf("/%s: summary %q does not end with its key %q", s.Name, s.Summary, s.Key)
		}
	}
	// Every one can be registered, so every one can be typed.
	if got := commands.NewRegistry(core).Len(); got != len(core) {
		t.Errorf("the registry took %d of %d core commands", got, len(core))
	}
}

// The rows the Commands mock draws.
func TestCoreSummariesInTheMock(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"statusline": "choose a preset or edit the status line",
		"status":     "connection, route and cache for this contact",
		"stop":       "stop the reply in flight (esc)",
	} {
		if got := find(t, name).Summary; got != want {
			t.Errorf("/%s summary %q, want %q", name, got, want)
		}
	}
}

func TestSourceCore(t *testing.T) {
	t.Parallel()
	if commands.SourceCore != "core" {
		t.Errorf("SourceCore = %q, want core", commands.SourceCore)
	}
}

func TestCoreReturnsItsOwnSlice(t *testing.T) {
	t.Parallel()
	first := commands.Core()
	first[0].Name = "changed"
	if got := commands.Core()[0].Name; got != "statusline" {
		t.Errorf("a caller's change reached the next call: %q", got)
	}
}
