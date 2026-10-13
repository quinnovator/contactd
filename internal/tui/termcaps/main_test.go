package termcaps_test

import (
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestMain(m *testing.M) { tuitest.Main(m) }

// xterm is an ordinary modern terminal with nothing special about it: 256
// colours, a UTF-8 locale. Tests change it a field at a time.
func xterm(change ...func(*termcaps.Env)) termcaps.Env {
	env := termcaps.Env{Term: "xterm-256color", Lang: "en_US.UTF-8", Detected: termcaps.Color256}
	for _, c := range change {
		c(&env)
	}
	return env
}

// tmux is the environment inside a tmux pane, as measured: tmux 3.7c sets
// TERM, TERM_PROGRAM and TMUX, and is detected as 256 colours even with
// COLORTERM=truecolor.
func tmux(change ...func(*termcaps.Env)) termcaps.Env {
	return xterm(append([]func(*termcaps.Env){func(e *termcaps.Env) {
		e.Term, e.TermProgram, e.ColorTerm = "tmux-256color", "tmux", "truecolor"
		e.Tmux = "/private/tmp/tmux-501/default,76907,1"
	}}, change...)...)
}

// kitty is the environment inside kitty.
func kitty(change ...func(*termcaps.Env)) termcaps.Env {
	return xterm(append([]func(*termcaps.Env){func(e *termcaps.Env) {
		e.Term, e.ColorTerm, e.Detected = "xterm-kitty", "truecolor", termcaps.ColorTrue
	}}, change...)...)
}

func term(v string) func(*termcaps.Env) { return func(e *termcaps.Env) { e.Term = v } }

func lang(v string) func(*termcaps.Env) { return func(e *termcaps.Env) { e.Lang = v } }

func detected(c termcaps.Color) func(*termcaps.Env) {
	return func(e *termcaps.Env) { e.Detected = c }
}

func noColor(v string) func(*termcaps.Env) { return func(e *termcaps.Env) { e.NoColor = v } }

func program(v string) func(*termcaps.Env) { return func(e *termcaps.Env) { e.TermProgram = v } }

// fold applies replies in order, as the client does.
func fold(in ...termcaps.Reply) termcaps.Replies {
	var r termcaps.Replies
	for _, reply := range in {
		r = termcaps.Fold(r, reply)
	}
	return r
}

func background(dark bool) termcaps.Reply {
	return termcaps.Reply{Kind: termcaps.ReplyBackground, Dark: dark}
}

func version(text string) termcaps.Reply {
	return termcaps.Reply{Kind: termcaps.ReplyVersion, Text: text}
}

func kittyReply(id int, ok bool) termcaps.Reply {
	return termcaps.Reply{Kind: termcaps.ReplyKitty, ID: id, OK: ok}
}

func cell(w, h int) termcaps.Reply {
	return termcaps.Reply{Kind: termcaps.ReplyCell, W: w, H: h}
}

func da1() termcaps.Reply { return termcaps.Reply{Kind: termcaps.ReplyDA1} }

func deadline() termcaps.Reply { return termcaps.Reply{Kind: termcaps.ReplyDeadline} }

func mode2027(ok bool) termcaps.Reply {
	return termcaps.Reply{Kind: termcaps.ReplyMode2027, OK: ok}
}

func keyboard(flags int) termcaps.Reply {
	return termcaps.Reply{Kind: termcaps.ReplyKeyboard, Flags: flags}
}
