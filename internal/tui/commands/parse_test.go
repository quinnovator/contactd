package commands_test

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/quinnovator/contactd/internal/tui/commands"
)

func TestParse(t *testing.T) {
	t.Parallel()
	message := func(text string) commands.Line {
		return commands.Line{Kind: commands.LineMessage, Message: text}
	}
	command := func(name, rest string, args ...string) commands.Line {
		return commands.Line{Kind: commands.LineCommand, Name: name, Rest: rest, Args: args}
	}
	refused := commands.Line{Kind: commands.LineRefused}
	tests := []struct {
		name, draft string
		want        commands.Line
	}{
		// Starts with "//": a message, without its first slash.
		{"two slashes send one", "//x", message("/x")},
		{"two slashes and words", "//etc/hosts is empty", message("/etc/hosts is empty")},
		{"two slashes over several lines", "//x\nmore", message("/x\nmore")},
		{"two slashes alone", "//", message("/")},
		{"three slashes", "///x", message("//x")},
		{"two slashes lose nothing but the first", "//  x  \"y\"\n\n /z\t.", message("/  x  \"y\"\n\n /z\t.")},
		{"two slashes, then a command-like line", "//\n/stop", message("/\n/stop")},

		// One line starting with "/": a command.
		{"a command", "/help", command("help", "")},
		{"the name is lower-cased, the rest is not", "/HELP Stop", command("help", "Stop", "Stop")},
		{"trailing white space is dropped", "/help \t\n\n", command("help", "")},
		{"the rest is trimmed", "/open   ada lovelace  ", command("open", "ada lovelace", "ada", "lovelace")},
		{"a tab ends the name", "/open\tada", command("open", "ada", "ada")},
		{"a slash alone", "/", command("", "")},
		{"a slash and a space", "/ help", command("", "help", "help")},
		{"a renamed mod command", "/cache.stats 7", command("cache.stats", "7", "7")},
		// The usage of /statusline is short for the ghost text; the name
		// after "preset" still arrives.
		{"a preset by name", "/statusline preset compact", command(
			"statusline", "preset compact", "preset", "compact",
		)},
		{"a path is not a second command", "/image save 12 /tmp/a.png", command(
			"image", "save 12 /tmp/a.png", "save", "12", "/tmp/a.png",
		)},
		{"two slashes later are an argument", "/x //y", command("x", "//y", "//y")},
		{"a slash and a tab", "/\t", command("", "")},
		{"a line break at the end only", "/stop\r\n", command("stop", "")},
		{"a no-break space ends the name", "/open\u00a0ada", command("open", "ada", "ada")},
		// U+2028 is white space, and the input box has no line for it.
		{"a line separator is white space", "/open a\u2028b", command("open", "a\u2028b", "a", "b")},
		{"a control byte is part of the name", "/x\x00y", command("x\x00y", "")},

		// Quoted arguments.
		{"quotes keep spaces", `/open "ada lovelace" now`, command(
			"open", `"ada lovelace" now`, "ada lovelace", "now",
		)},
		{"quotes after other arguments", `/image save 12.1 "my chart.png"`, command(
			"image", `save 12.1 "my chart.png"`, "save", "12.1", "my chart.png",
		)},
		{"quotes inside a word", `/x a"b c"d e`, command("x", `a"b c"d e`, "ab cd", "e")},
		{"an empty pair is an empty argument", `/x "" y`, command("x", `"" y`, "", "y")},
		{"an open quote runs to the end", `/x "open quote  runs`, command("x", `"open quote  runs`, "open quote  runs")},
		{"runs of spaces between arguments", "/x  a \t b", command("x", "a \t b", "a", "b")},
		{"a tab inside quotes is kept", "/x \"a\tb\"", command("x", "\"a\tb\"", "a\tb")},
		{"a byte that is not UTF-8 is kept", "/x a\xffb", command("x", "a\xffb", "a\xffb")},
		{"a backslash escapes nothing", `/open a\ "b\" c`, command("open", `a\ "b\" c`, `a\`, `b\`, "c")},
		{"single quotes are text", `/open 'a b'`, command("open", `'a b'`, "'a", "b'")},
		{"a quote alone", `/x "`, command("x", `"`, "")},
		{"an open quote keeps a later slash", `/x "a /b`, command("x", `"a /b`, "a /b")},

		// Several lines, the first starting with one "/": refused.
		{"two lines", "/help\nme", refused},
		{"a line break from another system", "/help\r\nme", refused},
		{"a blank line between", "/stop\n\nplease", refused},
		{"a slash alone, then a line", "/\nx", refused},
		{"a bare carriage return", "/help\rme", refused},
		{"two slashes on the second line", "/help\n//me", refused},
		{"a line break inside quotes", "/open \"ada\nlovelace\"", refused},

		// Anything else: a message.
		{"a message", "hello", message("hello")},
		{"a message keeps its leading space", "  hello", message("  hello")},
		{"a message loses its trailing space", "hello \n\t ", message("hello")},
		{"several lines", "one\ntwo", message("one\ntwo")},
		{"a slash after a space is text", " /help", message(" /help")},
		{"a slash inside is text", "see /help", message("see /help")},
		{"a slash on a later line is text", "see\n/help", message("see\n/help")},
		{"a slash after a blank line is text", "\n/help", message("\n/help")},
		{"nothing", "", message("")},
		{"only white space", " \n ", message("")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := commands.Parse(tt.draft)
			if got.Kind != tt.want.Kind || got.Name != tt.want.Name || got.Rest != tt.want.Rest ||
				got.Message != tt.want.Message || !slices.Equal(got.Args, tt.want.Args) {
				t.Errorf("Parse(%q) =\n%+v\nwant:\n%+v", tt.draft, got, tt.want)
			}
		})
	}
}

// Every draft of up to six pieces from a small alphabet: one that starts
// with one slash is never a message, and a message is the draft, less its
// trailing white space and, after two slashes, its first slash.
func TestParseNeverSendsACommand(t *testing.T) {
	t.Parallel()
	pieces := []string{"/", " ", "\t", "\n", "\r", "x", "S", `"`}
	var walk func(draft string, left int)
	walk = func(draft string, left int) {
		checkParse(t, draft)
		if left == 0 {
			return
		}
		for _, p := range pieces {
			walk(draft+p, left-1)
		}
	}
	walk("", 6)
}

// checkParse checks one draft against the four rows of the parsing table.
func checkParse(t *testing.T, draft string) {
	t.Helper()
	trimmed := strings.TrimRightFunc(draft, unicode.IsSpace)
	oneSlash := strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//")
	severalLines := strings.ContainsAny(trimmed, "\n\r")
	want := commands.LineMessage
	switch {
	case oneSlash && severalLines:
		want = commands.LineRefused
	case oneSlash:
		want = commands.LineCommand
	}
	line := commands.Parse(draft)
	if line.Kind != want {
		t.Fatalf("Parse(%q) has kind %d, want %d", draft, line.Kind, want)
	}
	switch line.Kind {
	case commands.LineMessage:
		sent := trimmed
		if strings.HasPrefix(trimmed, "//") {
			sent = trimmed[1:]
		}
		if line.Message != sent {
			t.Fatalf("Parse(%q) sends %q, want %q", draft, line.Message, sent)
		}
	case commands.LineCommand:
		// The name is the word after the slash, and the rest follows it.
		word, ok := strings.CutSuffix(trimmed[1:], line.Rest)
		if !ok || strings.ToLower(strings.TrimRightFunc(word, unicode.IsSpace)) != line.Name {
			t.Fatalf("Parse(%q) = %+v", draft, line)
		}
		if line.Rest != "" && !strings.HasSuffix(word, " ") && !strings.HasSuffix(word, "\t") {
			t.Fatalf("Parse(%q) = %+v: no white space between the name and the rest", draft, line)
		}
		if strings.ContainsFunc(line.Name, unicode.IsSpace) || line.Message != "" {
			t.Fatalf("Parse(%q) = %+v", draft, line)
		}
	case commands.LineRefused:
		if line.Name != "" || line.Rest != "" || line.Args != nil || line.Message != "" {
			t.Fatalf("Parse(%q) = %+v: a refused draft carries nothing", draft, line)
		}
	default:
		t.Fatalf("Parse(%q) has kind %d", draft, line.Kind)
	}
}

func TestComplete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		spec commands.Spec
		want string
	}{
		{find(t, "statusline"), "/statusline "},
		{find(t, "quit"), "/quit "},
		{commands.Spec{Name: "cache.stats", Source: "mod cache"}, "/cache.stats "},
	}
	for _, tt := range tests {
		t.Run(tt.spec.Name, func(t *testing.T) {
			t.Parallel()
			got := commands.Complete(tt.spec)
			if got != tt.want {
				t.Errorf("Complete(/%s) = %q, want %q", tt.spec.Name, got, tt.want)
			}
			// What tab writes runs the command it named, and shows its usage.
			if line := commands.Parse(got); line.Kind != commands.LineCommand || line.Name != tt.spec.Name || line.Rest != "" {
				t.Errorf("Parse(%q) = %+v", got, line)
			}
			if ghost := commands.Ghost(got, tt.spec); ghost != tt.spec.Usage {
				t.Errorf("Ghost(%q) = %q, want the usage %q", got, ghost, tt.spec.Usage)
			}
		})
	}
}

func TestGhost(t *testing.T) {
	t.Parallel()
	statusline, stop, open := find(t, "statusline"), find(t, "stop"), find(t, "open")
	tests := []struct {
		name, draft string
		spec        commands.Spec
		want        string
	}{
		// The name is being typed: the rest of it, two spaces, the usage.
		{"the mock", "/st", statusline, "atusline  preset | edit"},
		{"a slash alone", "/", statusline, "statusline  preset | edit"},
		{"typed in capitals", "/STA", statusline, "tusline  preset | edit"},
		{"the whole name, no space yet", "/statusline", statusline, "  preset | edit"},
		{"no usage, so the name alone", "/st", stop, "op"},
		{"the whole name of a command without usage", "/stop", stop, ""},
		{"a mod command", "/sta", commands.Spec{Name: "stats", Usage: "[days]"}, "ts  [days]"},

		// The name and a space are typed: the usage alone.
		{"name and a space", "/statusline ", statusline, "preset | edit"},
		{"name and several spaces", "/open  \t", open, "<contact>"},
		{"name in capitals and a space", "/Open ", open, "<contact>"},
		{"name and a space, no usage", "/stop ", stop, ""},

		// Nothing to draw.
		{"an argument has begun", "/statusline p", statusline, ""},
		{"the preset word is typed", "/statusline preset ", statusline, ""},
		{"an argument is typed", "/open ada ", open, ""},
		{"part of a name and a space", "/st ", statusline, ""},
		{"another command's name and a space", "/stop ", statusline, ""},
		{"the typed word is inside the name", "/tus", statusline, ""},
		{"the typed word is longer than the name", "/statuslines", statusline, ""},
		{"a message", "st", statusline, ""},
		{"nothing typed", "", statusline, ""},
		{"a slash after a space", " /st", statusline, ""},
		{"two slashes send a message", "//st", statusline, ""},
		{"several lines", "/st\nmore", statusline, ""},
		{"no command chosen", "/zz", commands.Spec{}, ""},
		{"no command chosen, a slash alone", "/", commands.Spec{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := commands.Ghost(tt.draft, tt.spec); got != tt.want {
				t.Errorf("Ghost(%q, /%s) = %q, want %q", tt.draft, tt.spec.Name, got, tt.want)
			}
		})
	}
}

// The draft and its ghost, read together, name the selected command for
// every one the palette can offer.
func TestGhostFinishesEveryMatch(t *testing.T) {
	t.Parallel()
	r := mock()
	for _, typed := range []string{"", "s", "st", "sta", "o", "re"} {
		for _, s := range r.Match(typed) {
			ghost := commands.Ghost("/"+typed, s)
			if !strings.HasPrefix(s.Name, typed) {
				if ghost != "" {
					t.Errorf("Ghost(/%s, /%s) = %q, want nothing: the name does not start with it", typed, s.Name, ghost)
				}
				continue
			}
			want := s.Name
			if s.Usage != "" {
				want += "  " + s.Usage
			}
			if got := typed + ghost; got != want {
				t.Errorf("/%s then its ghost reads %q, want %q", typed, got, want)
			}
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"", "hello", "/", "//", "//x", "/help", "/HELP me", "/open \"ada lovelace\" x", "/a\nb", "/x \"",
		" /x", "/x\r\ny", "/\xff \xfe\"", "/st ", "/statusline  ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, draft string) {
		trimmed := strings.TrimRightFunc(draft, unicode.IsSpace)
		line := commands.Parse(draft)
		switch line.Kind {
		case commands.LineMessage:
			// What is sent is the draft, or the draft without its first slash.
			if line.Message != trimmed && "/"+line.Message != trimmed {
				t.Errorf("Parse(%q) sends %q", draft, line.Message)
			}
			if strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//") {
				t.Errorf("Parse(%q) sends a draft that starts with one slash", draft)
			}
			if line.Name != "" || line.Rest != "" || line.Args != nil {
				t.Errorf("Parse(%q) = %+v: a message with a command's fields", draft, line)
			}
		case commands.LineCommand:
			if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") || strings.ContainsAny(trimmed, "\n\r") {
				t.Errorf("Parse(%q) is a command", draft)
			}
			if strings.ContainsFunc(line.Name, unicode.IsSpace) {
				t.Errorf("Parse(%q) has name %q", draft, line.Name)
			}
			if line.Rest != strings.TrimSpace(line.Rest) || !strings.HasSuffix(trimmed, line.Rest) {
				t.Errorf("Parse(%q) has rest %q", draft, line.Rest)
			}
			if n := len(strings.Fields(line.Rest)); len(line.Args) > n {
				t.Errorf("Parse(%q) has %d arguments from %d words", draft, len(line.Args), n)
			}
			for _, arg := range line.Args {
				if strings.Contains(arg, `"`) {
					t.Errorf("Parse(%q) has argument %q", draft, arg)
				}
			}
			if line.Message != "" {
				t.Errorf("Parse(%q) = %+v: a command with a message", draft, line)
			}
		case commands.LineRefused:
			if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") || !strings.ContainsAny(trimmed, "\n\r") {
				t.Errorf("Parse(%q) is refused", draft)
			}
		default:
			t.Errorf("Parse(%q) has kind %d", draft, line.Kind)
		}
		// A ghost is only ever drawn for a one-line command.
		spec := commands.Spec{Name: line.Name, Usage: "usage"}
		if ghost := commands.Ghost(draft, spec); ghost != "" && line.Kind != commands.LineCommand {
			t.Errorf("Ghost(%q) = %q for a draft that is not a command", draft, ghost)
		}
	})
}
