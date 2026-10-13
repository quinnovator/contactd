package mods

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// specManifest is the manifest as the design writes it.
const specManifest = `abi = 1
name = "cache"                  # ^[a-z][a-z0-9-]{0,31}$, equal to the directory name
version = "0.3.1"
description = "How much of each prompt the provider served from its cache."
module = "cache.wasm"           # a bare file name; at most 16 MiB
sha256 = ""                     # optional pin
read = ["model_calls", "messages"]   # closed list; anything else is a load error
budget = "5ms"                  # may only lower the host's

[[segment]]
name = "cache"
refresh = "model_call"          # model_call | turn | state | a duration of at least 1s

[[command]]
name = "stats"
usage = "[days]"
summary = "token use and cache share by day"

[[renderer]]
kind = "code:mermaid"           # code:<language> | tool:<tool name>; requires "messages" in read
`

func TestParseManifestReadsEveryField(t *testing.T) {
	t.Parallel()
	got, err := ParseManifest([]byte(specManifest), "cache")
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	want := Manifest{
		ABI:         1,
		Name:        "cache",
		Version:     "0.3.1",
		Description: "How much of each prompt the provider served from its cache.",
		Module:      "cache.wasm",
		Read:        []Grant{GrantModelCalls, GrantMessages},
		Budget:      5 * time.Millisecond,
		Segments:    []SegmentDecl{{Name: "cache", Refresh: "model_call"}},
		Commands:    []CommandDecl{{Name: "stats", Usage: "[days]", Summary: "token use and cache share by day"}},
		Renderers:   []RendererDecl{{Kind: "code:mermaid"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseManifest:\n got %+v\nwant %+v", got, want)
	}
}

// minimal is a manifest with only what is required, plus extra root keys and
// extra tables.
func minimal(root, tables string) string {
	return "abi = 1\nname = \"m\"\nmodule = \"m.wasm\"\n" + root + "\n" + tables
}

func TestParseManifestAccepts(t *testing.T) {
	t.Parallel()
	pin := strings.Repeat("AB", 32)
	cases := []struct {
		name  string
		src   string
		check func(Manifest) bool
	}{
		{"only what is required", minimal("", ""), func(m Manifest) bool {
			return m.Name == "m" && m.Module == "m.wasm" && m.Budget == 0 && len(m.Read) == 0 &&
				len(m.Segments)+len(m.Commands)+len(m.Renderers) == 0
		}},
		{"a key this client does not know", minimal(`homepage = "https://example.com"`, "[future]\nx = 1\n"), func(m Manifest) bool {
			return m.Name == "m"
		}},
		{
			"every grant, in any order and repeated", minimal(
				`read = ["clock", "workspace", "messages", "model_calls", "state", "route", "contact", "clock"]`, ""),
			func(m Manifest) bool { return reflect.DeepEqual(m.Read, Grants()) },
		},
		{"a pin, in upper case", minimal(`sha256 = "`+pin+`"`, ""), func(m Manifest) bool {
			return m.SHA256 == strings.ToLower(pin)
		}},
		{"a budget under the host's", minimal(`budget = "750us"`, ""), func(m Manifest) bool {
			return m.Budget == 750*time.Microsecond
		}},
		{"each kind of refresh", minimal("", `[[segment]]
name = "a"
refresh = "model_call"
[[segment]]
name = "b_2"
refresh = "turn"
[[segment]]
name = "c-3"
refresh = "state"
[[segment]]
name = "d"
refresh = "1s"
[[segment]]
name = "e"
refresh = "1m30s"
`), func(m Manifest) bool {
			every := make([]time.Duration, 0, len(m.Segments))
			for _, s := range m.Segments {
				every = append(every, s.Every())
			}
			return reflect.DeepEqual(every, []time.Duration{0, 0, 0, time.Second, 90 * time.Second})
		}},
		{
			"renderers of both kinds", minimal(`read = ["messages"]`,
				"[[renderer]]\nkind = \"code:c++\"\n[[renderer]]\nkind = \"tool:python\"\n"),
			func(m Manifest) bool {
				return reflect.DeepEqual(m.Renderers, []RendererDecl{{Kind: "code:c++"}, {Kind: "tool:python"}})
			},
		},
		{
			"text made safe to draw", minimal("version = \"1\\u001b[31m\"\ndescription = \"two\\nlines\"",
				"[[command]]\nname = \"c\"\nusage = \"\\u001b]0;t\\u0007x\"\nsummary = \"a\\tb\"\n"),
			func(m Manifest) bool {
				return m.Version == "1[31m" && m.Description == "two lines" &&
					m.Commands[0].Usage == "]0;tx" && m.Commands[0].Summary == "a    b"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := ParseManifest([]byte(tc.src), "m")
			if err != nil {
				t.Fatalf("ParseManifest: %v", err)
			}
			if !tc.check(m) {
				t.Errorf("ParseManifest = %+v", m)
			}
		})
	}
}

func TestParseManifestRefuses(t *testing.T) {
	t.Parallel()
	named := func(name string) string { return "abi = 1\nname = \"" + name + "\"\nmodule = \"m.wasm\"\n" }
	module := func(file string) string { return "abi = 1\nname = \"m\"\nmodule = \"" + file + "\"\n" }
	many := strings.Repeat("[[command]]\nname = \"c\"\n", 33)
	cases := []struct {
		name, dir, src, want string
	}{
		{"not TOML", "m", "abi = 1\nname = [\n", "mod.toml: line 2"},
		{"no abi", "m", "name = \"m\"\nmodule = \"m.wasm\"\n", "abi is missing"},
		{"another abi", "m", "abi = 2\nname = \"m\"\nmodule = \"m.wasm\"\n", "abi 2 is not supported"},
		{"abi as text", "m", "abi = \"1\"\n", "mod.toml: line 1"},

		{"no name", "m", "abi = 1\nmodule = \"m.wasm\"\n", `name "" must be`},
		{"a name in capitals", "Cache", named("Cache"), `name "Cache" must be`},
		{"a name that starts with a digit", "9lives", named("9lives"), `name "9lives" must be`},
		{"a name with a dot", "a.b", named("a.b"), `name "a.b" must be`},
		{"a name with an underscore", "a_b", named("a_b"), `name "a_b" must be`},
		{"a name with a slash", "x", named("../x"), `name "../x" must be`},
		{"a name of 33 letters", strings.Repeat("a", 33), named(strings.Repeat("a", 33)), "must be"},
		{"a name that is not the directory's", "git", named("cache"), `name "cache" must equal the directory name "git"`},

		{"no module", "m", "abi = 1\nname = \"m\"\n", "module is missing"},
		{"a module in another directory", "m", module("../evil.wasm"), "must be a bare file name"},
		{"a module with a path", "m", module("sub/m.wasm"), "must be a bare file name"},
		{"a module with an absolute path", "m", module("/tmp/m.wasm"), "must be a bare file name"},
		{"a hidden module", "m", module(".m.wasm"), "does not start with a dot"},
		{"the parent as a module", "m", module(".."), "does not start with a dot"},
		{"the manifest as a module", "m", module("mod.toml"), "module cannot be the manifest"},
		{"the manifest in capitals as a module", "m", module("Mod.TOML"), "module cannot be the manifest"},

		{"a pin that is too short", "m", minimal(`sha256 = "abc123"`, ""), "sha256 must be 64 hex digits"},
		{"a pin that is not hex", "m", minimal(`sha256 = "`+strings.Repeat("z", 64)+`"`, ""), "sha256 must be 64 hex digits"},

		{"an unknown grant", "m", minimal(`read = ["model_calls", "files"]`, ""), `line 4: read: unknown grant "files"`},
		{"a grant in capitals", "m", minimal(`read = ["Clock"]`, ""), `unknown grant "Clock"`},
		{"the network as a grant", "m", minimal(`read = ["network"]`, ""), `unknown grant "network"`},
		{"read as one string", "m", minimal(`read = "clock"`, ""), "line 4"},

		{"a budget that is not a duration", "m", minimal(`budget = "fast"`, ""), "line 4: budget must be a duration above zero"},
		{"a budget of nothing", "m", minimal(`budget = "0s"`, ""), "budget must be a duration above zero"},
		{"a budget below nothing", "m", minimal(`budget = "-1ms"`, ""), "budget must be a duration above zero"},
		{"a budget as a number", "m", minimal(`budget = 5`, ""), "line 4"},

		{
			"a renderer without messages", "m",
			minimal(`read = ["model_calls"]`, "[[renderer]]\nkind = \"code:mermaid\"\n"),
			`a renderer is handed message text: add "messages" to read`,
		},
		{"a renderer of an unknown kind", "m", minimal(`read = ["messages"]`, "[[renderer]]\nkind = \"image:png\"\n"), "kind must be code:<language> or tool:<tool name>"},
		{"a renderer of no language", "m", minimal(`read = ["messages"]`, "[[renderer]]\nkind = \"code:\"\n"), "kind must be"},
		{"a renderer kind with a space", "m", minimal(`read = ["messages"]`, "[[renderer]]\nkind = \"code:a b\"\n"), "kind must be"},
		{"a renderer with no kind", "m", minimal(`read = ["messages"]`, "[[renderer]]\n"), "line 5: kind must be"},
		{
			"a renderer declared twice", "m",
			minimal(`read = ["messages"]`, "[[renderer]]\nkind = \"code:x\"\n[[renderer]]\nkind = \"code:x\"\n"),
			`renderer "code:x" is declared twice`,
		},

		{"a segment with no name", "m", minimal("", "[[segment]]\nrefresh = \"turn\"\n"), "line 5: segment name must be"},
		{"a segment name with a space", "m", minimal("", "[[segment]]\nname = \"a b\"\nrefresh = \"turn\"\n"), "line 6: segment name must be"},
		{"a segment with no refresh", "m", minimal("", "[[segment]]\nname = \"a\"\n"), "refresh must be model_call, turn, state or a duration of at least 1s"},
		{"a refresh on an unknown event", "m", minimal("", "[[segment]]\nname = \"a\"\nrefresh = \"keypress\"\n"), "line 7: refresh must be"},
		{"a refresh under a second", "m", minimal("", "[[segment]]\nname = \"a\"\nrefresh = \"999ms\"\n"), "refresh must be"},
		{
			"a segment declared twice", "m",
			minimal("", "[[segment]]\nname = \"a\"\nrefresh = \"turn\"\n[[segment]]\nname = \"a\"\nrefresh = \"state\"\n"),
			`line 8: segment "a" is declared twice`,
		},

		{"a command with no name", "m", minimal("", "[[command]]\nusage = \"x\"\n"), "command name must be"},
		{"a command name with a slash", "m", minimal("", "[[command]]\nname = \"/stats\"\n"), "command name must be"},
		{"a command declared twice", "m", minimal("", "[[command]]\nname = \"c\"\n[[command]]\nname = \"c\"\n"), `command "c" is declared twice`},
		{"a usage that is not text", "m", minimal("", "[[command]]\nname = \"c\"\nusage = 3\n"), "line 7: usage"},
		{"too many commands", "m", minimal("", many), "more than 32 commands"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := ParseManifest([]byte(tc.src), tc.dir)
			if err == nil {
				t.Fatalf("ParseManifest accepted it: %+v", m)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
			if !reflect.DeepEqual(m, Manifest{}) {
				t.Errorf("a refused manifest came back as %+v, want the zero value", m)
			}
		})
	}
}

func TestBudgetAboveTheHosts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		budget string
		ok     bool
	}{
		{"", true},
		{`budget = "1ms"`, true},
		{`budget = "5ms"`, true},
		{`budget = "5001us"`, false},
		{`budget = "50ms"`, false},
		{`budget = "1h"`, false},
	}
	for _, tc := range cases {
		m, err := ParseManifest([]byte(minimal(tc.budget, "")), "m")
		if err != nil {
			t.Fatalf("%s: %v", tc.budget, err)
		}
		err = m.checkBudget(DefaultBudget)
		if (err == nil) != tc.ok {
			t.Errorf("%s against a host budget of 5ms: error %v, want ok=%v", tc.budget, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "above the host's 5ms") {
			t.Errorf("%s: error %q does not say what the host gives", tc.budget, err)
		}
	}
}

func TestGrantsAreTheSevenOfTheABI(t *testing.T) {
	t.Parallel()
	want := []Grant{"contact", "route", "state", "model_calls", "messages", "workspace", "clock"}
	if got := Grants(); !reflect.DeepEqual(got, want) {
		t.Errorf("Grants() = %v, want %v", got, want)
	}
}
