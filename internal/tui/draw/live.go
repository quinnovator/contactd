package draw

import (
	"strconv"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/transcript"
)

// liveText is what was drawn of the reply in flight the last time. A reply
// arrives a few words at a time, and only its last line can still change: the
// lines before it are wrapped once and kept.
type liveText struct {
	// done is the reply up to and with its last line break, and lines is
	// that much of it drawn.
	done  string
	lines []string
}

// Live draws the turn in flight: what the agent has written so far, the tool
// call it has begun to write, and the tool that is running. running is how
// long that tool has run.
//
// The text is shown as written, wrapped: rendering Markdown again on every
// fragment would make it jump. A running tool is one row, which belongs
// directly under the tool call it is about.
//
// The lines carry no blank line above them. The caller decides whether one
// belongs between the last block and these.
func (r *Renderer) Live(l transcript.Live, running time.Duration) []string {
	var out []string
	if strings.TrimSpace(l.Text) != "" {
		out = r.streamed(l.Text)
	}
	if l.Starting != "" {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, r.tone(toneTool, r.th.G.Tool+" "+r.toolName(l.Starting))+r.tone(toneDim, " "+r.th.G.Ellipsis))
	}
	if l.Running != "" {
		if len(out) > 0 && l.Starting == "" {
			out = append(out, "")
		}
		row := r.th.G.Elbow + " running" + r.th.G.Ellipsis + " " + seconds(running)
		out = append(out, strings.Repeat(" ", indent)+r.tone(toneTool, row))
	}
	return r.fit(out)
}

// streamed draws the text of the reply in flight. The lines of the text that
// are finished are taken from the last call when the text still begins the
// same way; only what came after them is wrapped.
func (r *Renderer) streamed(text string) []string {
	lt := &r.live
	if !strings.HasPrefix(text, lt.done) {
		*lt = liveText{}
	}
	rest := text[len(lt.done):]
	if end := strings.LastIndexByte(rest, '\n'); end >= 0 {
		lt.lines = append(lt.lines, r.plain(rest[:end])...)
		lt.done = text[:len(lt.done)+end+1]
		rest = rest[end+1:]
	}
	// The result is a new slice every time: the caller keeps it, and the
	// kept lines grow.
	out := make([]string, 0, len(lt.lines)+1)
	out = append(out, lt.lines...)
	if rest != "" {
		out = append(out, r.plain(rest)...)
	}
	return trimBlank(out)
}

// plain wraps whole lines of the reply in flight, inside the indent.
func (r *Renderer) plain(text string) []string {
	r.n.liveWraps += strings.Count(text, "\n") + 1
	return r.indented("", r.th.Wrap(r.clean(text), r.inner))
}

// seconds writes how long a tool has run: "2.1s", and "1m05s" from a minute
// up. Tenths are cut, not rounded, so the figure never runs ahead.
func seconds(d time.Duration) string {
	d = max(d, 0)
	if d < time.Minute {
		tenths := int64(d / (100 * time.Millisecond))
		return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10) + "s"
	}
	secs := int64(d / time.Second)
	return strconv.FormatInt(secs/60, 10) + "m" + strconv.FormatInt(secs%60/10, 10) + strconv.FormatInt(secs%60%10, 10) + "s"
}
