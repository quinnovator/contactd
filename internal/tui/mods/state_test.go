package mods

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateFileRoundTrip(t *testing.T) {
	t.Parallel()
	want := state{
		"cache": {
			on: true, source: "/Users/jack/src/contactd/dist/mods/cache",
			sha256: strings.Repeat("ab", 32),
		},
		"git": {on: false, sha256: strings.Repeat("0", 64)},
		"slow-one": {
			source:   `C:\mods\"quoted" and a tab	here`,
			disabled: "over budget 3 times in a row, 2026-10-12 12:04",
		},
		"bare": {},
	}
	path := filepath.Join(t.TempDir(), "sub", "mods.toml")
	if err := saveState(path, want); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if !maps.Equal(got, want) {
		t.Errorf("the state read back:\n got %+v\nwant %+v", got, want)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mods.toml mode %v (%v), want 0600", fi.Mode(), err)
	}
}

func TestStateFileIsWrittenAsTheDesignShows(t *testing.T) {
	t.Parallel()
	st := state{
		"git": {},
		"cache": {
			on: true, source: "/Users/jack/src/contactd/dist/mods/cache", sha256: "abc",
			disabled: "",
		},
	}
	const want = `[mod.cache]
on = true
source = "/Users/jack/src/contactd/dist/mods/cache"
sha256 = "abc"
disabled = ""

[mod.git]
on = false
source = ""
sha256 = ""
disabled = ""
`
	if got := string(st.encode()); got != want {
		t.Errorf("encode:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		want state
		err  string
	}{
		{name: "an empty file", src: "", want: state{}},
		{
			name: "the file as the design writes it",
			src: `[mod.cache]
on = true
source = "/Users/jack/src/contactd/dist/mods/cache"   # where it was installed from
sha256 = "abc"      # of the module when it was switched on
disabled = ""       # "over budget 3 times in a row, 2026-10-12 12:04"
`,
			want: state{"cache": {on: true, source: "/Users/jack/src/contactd/dist/mods/cache", sha256: "abc"}},
		},
		{
			name: "keys left out are off and empty",
			src:  "[mod.a]\n[mod.b]\non = true\n",
			want: state{"a": {}, "b": {on: true}},
		},
		{
			name: "what the client does not know is skipped",
			src:  "theme = \"dark\"\n[mod.a]\non = true\nfuture = 3\n[other]\non = true\n[mod.a.b]\non = true\n[mod.Caps]\non = true\n[[mod.c]]\non = true\n",
			want: state{"a": {on: true}},
		},
		{
			name: "a reason is made safe to draw",
			src:  "[mod.a]\ndisabled = \"over\\u001b[31m budget\\nagain\"\n",
			want: state{"a": {disabled: "over[31m budget again"}},
		},
		{name: "on is not a boolean", src: "[mod.a]\non = \"yes\"\n", err: "line 2"},
		{name: "source is not a string", src: "[mod.a]\nsource = 3\n", err: "line 2"},
		{name: "not TOML", src: "[mod.a\n", err: "line 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseState([]byte(tc.src))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error = %v, want one containing %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseState: %v", err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("parseState:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestStateWithNoPathIsNotKept(t *testing.T) {
	t.Parallel()
	if err := saveState("", state{"a": {on: true}}); err != nil {
		t.Errorf("saveState with no path: %v", err)
	}
	st, err := loadState("")
	if err != nil || len(st) != 0 {
		t.Errorf("loadState with no path = %+v, %v", st, err)
	}
	st, err = loadState(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || st == nil || len(st) != 0 {
		t.Errorf("loadState of a missing file = %+v, %v; want an empty state", st, err)
	}
	if _, err := loadState(t.TempDir()); err == nil {
		t.Error("loadState of a directory succeeded")
	}
}
