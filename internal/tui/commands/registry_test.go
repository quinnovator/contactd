package commands_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/commands"
)

// mock returns the registry the Commands mock shows: the core commands and
// four from mods, added out of order.
func mock() *commands.Registry {
	r := commands.NewRegistry(commands.Core())
	r.SetMod("standup", []commands.Spec{{Name: "standup", Summary: "summarize yesterday for the team"}})
	r.SetMod("git", []commands.Spec{{Name: "over"}, {Name: "todo"}})
	r.SetMod("cache", []commands.Spec{{Name: "stats", Usage: "[days]", Summary: "token use and cache share by day"}})
	return r
}

func TestMatch(t *testing.T) {
	t.Parallel()
	core := names(commands.Core())
	tests := []struct {
		name, typed string
		want        []string
	}{
		{"the mock: core in declared order, then mods by name", "st", []string{
			"statusline", "status", "stop", "standup", "stats",
		}},
		{"any case", "ST", []string{"statusline", "status", "stop", "standup", "stats"}},
		{"nothing typed lists everything", "", append(slices.Clone(core), "over", "standup", "stats", "todo")},
		{"names that start with it, then names that contain it", "o", []string{
			"open", "over",
			"stop", "contacts", "mods", "color", "top", "bottom", "reload", "todo",
		}},
		{"a whole name", "statusline", []string{"statusline"}},
		{"only inside a name", "tus", []string{"statusline", "status"}},
		{"a mod command only", "stand", []string{"standup"}},
		{"no match", "zzz", nil},
		{"longer than any name", "statuslines", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := names(mock().Match(tt.typed)); !slices.Equal(got, tt.want) {
				t.Errorf("Match(%q) = %v, want %v", tt.typed, got, tt.want)
			}
		})
	}
}

// Mod commands sort by their own names, whatever the mod is called and
// whenever it was added.
func TestMatchSortsModCommandsByName(t *testing.T) {
	t.Parallel()
	r := commands.NewRegistry(nil)
	r.SetMod("alpha", []commands.Spec{{Name: "zulu"}, {Name: "mike"}})
	r.SetMod("zeta", []commands.Spec{{Name: "alpha"}, {Name: "november"}})
	if got, want := names(r.Match("")), []string{"alpha", "mike", "november", "zulu"}; !slices.Equal(got, want) {
		t.Errorf("Match(\"\") = %v, want %v", got, want)
	}
}

func TestMatchReturnsItsOwnSlice(t *testing.T) {
	t.Parallel()
	r := mock()
	got := r.Match("st")
	got[0].Name = "changed"
	if again := r.Match("st"); again[0].Name != "statusline" {
		t.Errorf("a caller's change reached the registry: %q", again[0].Name)
	}
}

func TestSetModRenamesAClash(t *testing.T) {
	t.Parallel()
	r := mock()
	renamed := r.SetMod("notes", []commands.Spec{
		{Name: "stop", Summary: "stop the timer"}, // core has it
		{Name: "stats"}, // the cache mod has it
		{Name: "jot", Usage: "<text>", NeedsArg: true},
	})
	if want := []string{"notes.stop", "notes.stats"}; !slices.Equal(renamed, want) {
		t.Errorf("renamed = %v, want %v", renamed, want)
	}
	tests := []struct {
		name string
		want commands.Spec
	}{
		{"stop", find(t, "stop")},
		{"notes.stop", commands.Spec{Name: "notes.stop", Summary: "stop the timer", Source: "mod notes"}},
		{"stats", commands.Spec{
			Name: "stats", Usage: "[days]", Summary: "token use and cache share by day", Source: "mod cache",
		}},
		{"notes.stats", commands.Spec{Name: "notes.stats", Source: "mod notes"}},
		{"jot", commands.Spec{Name: "jot", Usage: "<text>", Source: "mod notes", NeedsArg: true}},
	}
	for _, tt := range tests {
		if got, ok := r.Lookup(tt.name); !ok || got != tt.want {
			t.Errorf("Lookup(%q) = %+v, %t; want %+v", tt.name, got, ok, tt.want)
		}
	}
	if _, ok := r.Lookup("notes.jot"); ok {
		t.Error("a name nobody else has was registered under the mod's name too")
	}
	// A renamed command is found by either half of its name, after the names
	// that start with what was typed.
	if got, want := names(r.Match("st")), []string{
		"statusline", "status", "stop", "standup", "stats", "notes.stats", "notes.stop",
	}; !slices.Equal(got, want) {
		t.Errorf("Match(\"st\") = %v, want %v", got, want)
	}
	if got, want := names(r.Match("notes")), []string{"notes.stats", "notes.stop"}; !slices.Equal(got, want) {
		t.Errorf("Match(\"notes\") = %v, want %v", got, want)
	}
}

func TestSetModReplacesTheModsCommands(t *testing.T) {
	t.Parallel()
	r := mock()
	size, rev := r.Len(), r.Rev()
	if renamed := r.SetMod("git", []commands.Spec{{Name: "branch"}}); renamed != nil {
		t.Errorf("renamed = %v, want none", renamed)
	}
	for _, gone := range []string{"over", "todo"} {
		if _, ok := r.Lookup(gone); ok {
			t.Errorf("/%s is still there after its mod was set again", gone)
		}
	}
	if got, ok := r.Lookup("branch"); !ok || got.Source != "mod git" {
		t.Errorf("Lookup(branch) = %+v, %t", got, ok)
	}
	if got, want := r.Len(), size-1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
	if r.Rev() == rev {
		t.Error("Rev did not change when a mod's commands did")
	}
	// A mod's own earlier names do not count as a clash with itself.
	if renamed := r.SetMod("git", []commands.Spec{{Name: "branch"}, {Name: "log"}}); renamed != nil {
		t.Errorf("renamed = %v after setting the same mod again, want none", renamed)
	}
	if _, ok := r.Lookup("git.branch"); ok {
		t.Error("a mod's command clashed with its own earlier registration")
	}
}

func TestSetModLeavesOutWhatCannotBeRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		specs []commands.Spec
		want  []string
	}{
		{"lower-cased", []commands.Spec{{Name: "Stats"}}, []string{"stats"}},
		{"letters, digits, dash and underscore", []commands.Spec{{Name: "day-2_x"}, {Name: "A9"}, {Name: "-"}}, []string{
			"-", "a9", "day-2_x",
		}},
		{"a letter outside ASCII", []commands.Spec{{Name: "d\u00eda"}, {Name: "ok"}}, []string{"ok"}},
		{"a digit outside ASCII", []commands.Spec{{Name: "day\u0663"}, {Name: "ok"}}, []string{"ok"}},
		// U+0455 and U+043E are Cyrillic, U+FF53 is full width: each is drawn
		// like the Latin letter and would pass for /stop.
		{"a letter that looks like another", []commands.Spec{
			{Name: "\u0455top"}, {Name: "st\u043ep"}, {Name: "\uff53top"}, {Name: "ok"},
		}, []string{"ok"}},
		// U+3164, U+115F and U+1160 are letters that draw nothing.
		{"a letter that draws nothing", []commands.Spec{{Name: "\u3164"}, {Name: "\u115f\u1160"}, {Name: "ok"}}, []string{"ok"}},
		// U+212A, the kelvin sign, lower-cases to a plain k.
		{"a capital outside ASCII", []commands.Spec{{Name: "\u212a"}, {Name: "ok"}}, []string{"ok"}},
		{"a byte that is not UTF-8", []commands.Spec{{Name: "a\xffb"}, {Name: "ok"}}, []string{"ok"}},
		{"no name", []commands.Spec{{Name: ""}, {Name: "ok"}}, []string{"ok"}},
		{"a space", []commands.Spec{{Name: "two words"}, {Name: "ok"}}, []string{"ok"}},
		{"a slash", []commands.Spec{{Name: "/stats"}, {Name: "ok"}}, []string{"ok"}},
		{"a dot, which marks a renamed command", []commands.Spec{{Name: "a.b"}, {Name: "ok"}}, []string{"ok"}},
		{"an escape sequence", []commands.Spec{{Name: "x\x1b[31m"}, {Name: "ok"}}, []string{"ok"}},
		{"said twice, in any case", []commands.Spec{{Name: "ok", Usage: "first"}, {Name: "OK", Usage: "second"}}, []string{"ok"}},
		{"nothing", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := commands.NewRegistry(nil)
			if renamed := r.SetMod("m", tt.specs); renamed != nil {
				t.Errorf("renamed = %v, want none", renamed)
			}
			if got := names(r.Match("")); !slices.Equal(got, tt.want) {
				t.Errorf("registered %v, want %v", got, tt.want)
			}
			if got, want := r.Len(), len(tt.want); got != want {
				t.Errorf("Len() = %d, want %d", got, want)
			}
		})
	}
}

// A renamed command is registered under a name that can be typed: the mod's
// name goes into it in lower case, as Parse and Lookup read it.
func TestSetModRenamesInLowerCase(t *testing.T) {
	t.Parallel()
	r := commands.NewRegistry(commands.Core())
	renamed := r.SetMod("Cache", []commands.Spec{{Name: "Stop", Usage: "now"}})
	if want := []string{"cache.stop"}; !slices.Equal(renamed, want) {
		t.Fatalf("renamed = %v, want %v", renamed, want)
	}
	want := commands.Spec{Name: "cache.stop", Usage: "now", Source: "mod Cache"}
	for _, typed := range []string{"cache.stop", "Cache.stop", "CACHE.STOP"} {
		if got, ok := r.Lookup(typed); !ok || got != want {
			t.Errorf("Lookup(%q) = %+v, %t; want %+v", typed, got, ok, want)
		}
	}
	if got, want := names(r.Match("cache")), []string{"cache.stop"}; !slices.Equal(got, want) {
		t.Errorf("Match(\"cache\") = %v, want %v", got, want)
	}
	r.RemoveMod("Cache")
	if _, ok := r.Lookup("cache.stop"); ok {
		t.Error("/cache.stop is still there after its mod was removed")
	}
}

// A clash is left out when "<mod>.<name>" could not be typed. The mod's
// other commands are kept.
func TestSetModLeavesOutAClashItCannotName(t *testing.T) {
	t.Parallel()
	for _, mod := range []string{"", "my mod", "a.b", "a/b", "x\x1b[31m", "d\u00eda", "tab\t"} {
		t.Run(mod, func(t *testing.T) {
			t.Parallel()
			r := commands.NewRegistry(commands.Core())
			size := r.Len()
			renamed := r.SetMod(mod, []commands.Spec{{Name: "stop"}, {Name: "jot"}})
			if renamed != nil {
				t.Errorf("renamed = %v, want none", renamed)
			}
			if got, want := r.Len(), size+1; got != want {
				t.Errorf("Len() = %d, want %d", got, want)
			}
			if got, ok := r.Lookup("jot"); !ok || got.Source != "mod "+mod {
				t.Errorf("Lookup(jot) = %+v, %t; want it from the mod", got, ok)
			}
			if got, want := names(r.Match("stop")), []string{"stop"}; !slices.Equal(got, want) {
				t.Errorf("Match(\"stop\") = %v, want %v", got, want)
			}
			canRunAll(t, r)
		})
	}
}

// Two mods whose names differ only by case cannot both rename one clash:
// the first keeps the name, and the second's command is left out.
func TestSetModCaseTwinsDoNotShareAName(t *testing.T) {
	t.Parallel()
	r := commands.NewRegistry(commands.Core())
	if got, want := r.SetMod("a", []commands.Spec{{Name: "stop", Usage: "first"}}), []string{"a.stop"}; !slices.Equal(got, want) {
		t.Fatalf("renamed = %v, want %v", got, want)
	}
	size, rev := r.Len(), r.Rev()
	if got := r.SetMod("A", []commands.Spec{{Name: "stop", Usage: "second"}}); got != nil {
		t.Errorf("renamed = %v for the twin, want none", got)
	}
	want := commands.Spec{Name: "a.stop", Usage: "first", Source: "mod a"}
	if got, ok := r.Lookup("A.stop"); !ok || got != want {
		t.Errorf("Lookup(A.stop) = %+v, %t; want %+v", got, ok, want)
	}
	if r.Len() != size || r.Rev() != rev {
		t.Errorf("Len() = %d and Rev moved %t; want %d and no change", r.Len(), r.Rev() != rev, size)
	}
	// The twin's other commands are its own, and removing it takes only those.
	r.SetMod("A", []commands.Spec{{Name: "stop"}, {Name: "jot"}})
	canRunAll(t, r)
	r.RemoveMod("A")
	if got, ok := r.Lookup("a.stop"); !ok || got != want {
		t.Errorf("after the twin was removed: Lookup(a.stop) = %+v, %t; want %+v", got, ok, want)
	}
	if _, ok := r.Lookup("jot"); ok {
		t.Error("/jot is still there after its mod was removed")
	}
}

// canRunAll checks that every registered command has a name of its own that
// can be typed: what tab writes for it parses back to it and is found.
func canRunAll(t *testing.T, r *commands.Registry) {
	t.Helper()
	all := r.Match("")
	if got, want := len(all), r.Len(); got != want {
		t.Errorf("Match(\"\") lists %d commands, Len() = %d", got, want)
	}
	seen := make(map[string]bool, len(all))
	for _, s := range all {
		if seen[s.Name] {
			t.Errorf("/%s is registered twice", s.Name)
		}
		seen[s.Name] = true
		line := commands.Parse(commands.Complete(s))
		if line.Kind != commands.LineCommand || line.Name != s.Name {
			t.Errorf("/%s cannot be typed: it parses as %+v", s.Name, line)
		}
		if got, ok := r.Lookup(line.Name); !ok || got != s {
			t.Errorf("Lookup(%q) = %+v, %t; want %+v", line.Name, got, ok, s)
		}
		for part := range strings.SplitSeq(s.Name, ".") {
			if !commands.ValidName(part) {
				t.Errorf("/%s: %q is not a valid name", s.Name, part)
			}
		}
	}
}

// Whatever mods ask for, in whatever order, every command registered can be
// run and every name SetMod reports is registered.
func TestEveryRegisteredCommandCanBeRun(t *testing.T) {
	t.Parallel()
	mods := []string{"cache", "Cache", "git", "a.b", "", "core", "stop", "two words"}
	lists := [][]commands.Spec{
		{{Name: "stop"}, {Name: "stats"}, {Name: "Help"}},
		{{Name: "stats"}, {Name: "cache"}, {Name: "git"}},
		{{Name: "STOP"}, {Name: "stop"}, {Name: "a.b"}, {Name: "d\u00eda"}},
		nil,
	}
	r := commands.NewRegistry(commands.Core())
	// A fixed walk over every pairing, several times, with removals between.
	for step := range 4 * len(mods) * len(lists) {
		mod := mods[(step*3)%len(mods)]
		if step%7 == 6 {
			r.RemoveMod(mod)
		} else {
			for _, name := range r.SetMod(mod, lists[(step*5+step/len(mods))%len(lists)]) {
				if got, ok := r.Lookup(name); !ok || got.Name != name || got.Source != "mod "+mod {
					t.Fatalf("step %d: SetMod(%q) renamed to %q, and Lookup gives %+v, %t", step, mod, name, got, ok)
				}
			}
		}
		canRunAll(t, r)
		if t.Failed() {
			t.Fatalf("step %d: after mod %q", step, mod)
		}
	}
	for _, s := range commands.Core() {
		if got, ok := r.Lookup(s.Name); !ok || got != s {
			t.Errorf("core /%s was displaced: %+v, %t", s.Name, got, ok)
		}
	}
}

func TestValidName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want bool
	}{
		{"stats", true},
		{"Stats", true},
		{"day-2_x", true},
		{"7", true},
		{"-", true},
		{"", false},
		{"two words", false},
		{"tab\t", false},
		{"/stats", false},
		{"cache.stats", false},
		{"x\x1b[31m", false},
		{"x\x00", false},
		{"d\u00eda", false},
		{"\u0455top", false}, // a Cyrillic letter drawn like s
		{"\u3164", false},    // a letter that draws nothing
		{"\u212a", false},    // the kelvin sign, which lower-cases to k
		{"a\xffb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := commands.ValidName(tt.name); got != tt.want {
				t.Errorf("ValidName(%q) = %t, want %t", tt.name, got, tt.want)
			}
		})
	}
}

// Of two commands a mod gives one name, the first is the one kept.
func TestSetModKeepsTheFirstOfARepeat(t *testing.T) {
	t.Parallel()
	r := commands.NewRegistry(commands.Core())
	renamed := r.SetMod("m", []commands.Spec{{Name: "stop", Usage: "first"}, {Name: "stop", Usage: "second"}})
	if want := []string{"m.stop"}; !slices.Equal(renamed, want) {
		t.Errorf("renamed = %v, want %v", renamed, want)
	}
	if got, _ := r.Lookup("m.stop"); got.Usage != "first" {
		t.Errorf("kept usage %q, want the first", got.Usage)
	}
}

// However many mods want one name, every command stays reachable under a
// name of its own.
func TestSetModNeverLosesAClash(t *testing.T) {
	t.Parallel()
	r := commands.NewRegistry(commands.Core())
	size := r.Len()
	mods := []string{"b", "a", "a-b", "c"}
	for round := range 2 {
		for _, mod := range mods {
			r.SetMod(mod, []commands.Spec{{Name: "stop"}, {Name: "stats"}, {Name: "b"}})
		}
		if got, want := r.Len(), size+3*len(mods); got != want {
			t.Fatalf("round %d: Len() = %d, want %d", round, got, want)
		}
		seen := make(map[string]bool)
		for _, s := range r.Match("") {
			if seen[s.Name] {
				t.Errorf("round %d: /%s is registered twice", round, s.Name)
			}
			seen[s.Name] = true
			if got, ok := r.Lookup(s.Name); !ok || got != s {
				t.Errorf("round %d: Lookup(%q) = %+v, %t; want %+v", round, s.Name, got, ok, s)
			}
		}
	}
	// The first mod to ask has the plain name; the rest have their own.
	for name, source := range map[string]string{
		"stop": "core", "b.stop": "mod b", "a.stop": "mod a", "a-b.stop": "mod a-b", "c.stop": "mod c",
		"stats": "mod b", "a.stats": "mod a", "b": "mod b", "a.b": "mod a", "c.b": "mod c",
	} {
		if got, ok := r.Lookup(name); !ok || got.Source != source {
			t.Errorf("Lookup(%q) = %+v, %t; want it from %q", name, got, ok, source)
		}
	}
}

func TestRemoveMod(t *testing.T) {
	t.Parallel()
	r := mock()
	r.SetMod("notes", []commands.Spec{{Name: "stats"}, {Name: "jot"}})
	size, rev := r.Len(), r.Rev()

	r.RemoveMod("cache")
	if _, ok := r.Lookup("stats"); ok {
		t.Error("/stats is still there after its mod was removed")
	}
	if got, want := r.Len(), size-1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
	if r.Rev() == rev {
		t.Error("Rev did not change when a mod was removed")
	}
	// Removing one mod leaves the others' commands alone, under the names
	// they already had.
	if got, want := names(r.Match("st")), []string{
		"statusline", "status", "stop", "standup", "notes.stats",
	}; !slices.Equal(got, want) {
		t.Errorf("Match(\"st\") = %v, want %v", got, want)
	}
	if _, ok := r.Lookup("jot"); !ok {
		t.Error("another mod's command went too")
	}
	// Set again, the mod gets the name that is now free.
	if renamed := r.SetMod("notes", []commands.Spec{{Name: "stats"}, {Name: "jot"}}); renamed != nil {
		t.Errorf("renamed = %v once the name was free, want none", renamed)
	}
	if got, ok := r.Lookup("stats"); !ok || got.Source != "mod notes" {
		t.Errorf("Lookup(stats) = %+v, %t; want the notes mod's", got, ok)
	}

	// A core command is not a mod's to remove.
	r.RemoveMod("core")
	r.RemoveMod("")
	if got, want := names(r.Match(""))[:len(commands.Core())], names(commands.Core()); !slices.Equal(got, want) {
		t.Errorf("core commands after removals: %v", got)
	}
}

func TestRevChangesOnlyWhenTheCommandsDo(t *testing.T) {
	t.Parallel()
	specs := []commands.Spec{{Name: "stats", Usage: "[days]"}}
	steps := []struct {
		name    string
		do      func(r *commands.Registry)
		changes bool
	}{
		{"a mod is added", func(r *commands.Registry) { r.SetMod("cache", specs) }, true},
		{"the same commands again", func(r *commands.Registry) { r.SetMod("cache", specs) }, false},
		{"a usage changes", func(r *commands.Registry) {
			r.SetMod("cache", []commands.Spec{{Name: "stats", Usage: "[weeks]"}})
		}, true},
		{"reading", func(r *commands.Registry) {
			r.Match("st")
			r.Lookup("stats")
			r.Len()
		}, false},
		{"a mod with no commands", func(r *commands.Registry) { r.SetMod("empty", nil) }, false},
		{"removing a mod that has none", func(r *commands.Registry) { r.RemoveMod("empty") }, false},
		{"removing a mod", func(r *commands.Registry) { r.RemoveMod("cache") }, true},
		{"removing it again", func(r *commands.Registry) { r.RemoveMod("cache") }, false},
	}
	r := commands.NewRegistry(commands.Core())
	for _, step := range steps {
		before := r.Rev()
		step.do(r)
		if got := r.Rev() != before; got != step.changes {
			t.Errorf("%s: Rev changed = %t, want %t", step.name, got, step.changes)
		}
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		want   string // the name found, or empty
		source string
	}{
		{"status", "status", "core"},
		{"STATUS", "status", "core"},
		{"stats", "stats", "mod cache"},
		{"st", "", ""},          // a prefix is not a name
		{"statuses", "", ""},    // nor is a longer word
		{"/status", "", ""},     // the name has no slash
		{"", "", ""},            // a slash alone is no command
		{"cache.stats", "", ""}, // only a clash is registered under the mod's name
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := mock().Lookup(tt.name)
			if ok != (tt.want != "") || got.Name != tt.want || got.Source != tt.source {
				t.Errorf("Lookup(%q) = %+v, %t; want name %q from %q", tt.name, got, ok, tt.want, tt.source)
			}
		})
	}
}

func TestLen(t *testing.T) {
	t.Parallel()
	if got, want := commands.NewRegistry(nil).Len(), 0; got != want {
		t.Errorf("empty registry: Len() = %d, want %d", got, want)
	}
	// The mock's count: 19 core commands and four from mods.
	if got, want := mock().Len(), 23; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

func TestNewRegistry(t *testing.T) {
	t.Parallel()
	given := []commands.Spec{
		{Name: "Beta", Source: "elsewhere"},
		{Name: "alpha"},
		{Name: "beta", Usage: "again"},
		{Name: "not a word"},
		{Name: "d\u00eda"},
		{Name: "a.b"},
		{Name: ""},
	}
	r := commands.NewRegistry(given)
	want := []commands.Spec{{Name: "beta", Source: "core"}, {Name: "alpha", Source: "core"}}
	if got := r.Match(""); !slices.Equal(got, want) {
		t.Errorf("registered %+v, want %+v", got, want)
	}
	// The registry keeps its own copy.
	given[1].Name = "changed"
	if _, ok := r.Lookup("alpha"); !ok {
		t.Error("a caller's change to its slice reached the registry")
	}
}
