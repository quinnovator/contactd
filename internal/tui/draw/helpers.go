package draw

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/transcript"
	"github.com/quinnovator/contactd/internal/tui/helperboard"
)

func a2aMethod(tool string) string {
	switch tool {
	case "start_helper", "message_helper":
		return "SendMessage"
	case "stop_helper":
		return "CancelTask"
	case "list_helpers":
		return "GetTask"
	}
	return ""
}

func (r *Renderer) helperCall(it *transcript.Item) ([]string, bool) {
	method := a2aMethod(it.Tool)
	if method == "" && it.Tool != "stay_quiet" {
		return nil, false
	}
	var a struct {
		Title    string `json:"title"`
		Brief    string `json:"brief"`
		Text     string `json:"text"`
		Every    string `json:"every"`
		Duration string `json:"duration"`
		Reason   string `json:"reason"`
	}
	if json.Unmarshal([]byte(it.Text), &a) != nil {
		return nil, false
	}
	if it.Tool == "stay_quiet" {
		return []string{r.tone(toneDim, r.th.G.Mid+" "+r.clean(r.o.Contact)+" stayed quiet: "+r.clean(a.Reason))}, true
	}
	line := r.th.G.Wire + " A2A " + method + " " + r.th.G.Arrow + " contactd helpers"
	if a.Title != "" {
		line += " " + r.th.G.Mid + " " + fmt.Sprintf("%q", r.clean(a.Title))
	}
	out := []string{r.tone(toneTool, line)}
	if a.Every != "" || a.Duration != "" {
		out = append(out, "  "+r.tone(toneDim, r.clean("every "+a.Every+" for "+a.Duration)))
	}
	text := a.Brief
	if text == "" {
		text = a.Text
	}
	rows, _ := r.folded(r.clean(text), max(1, r.w-2), foldedCode)
	for _, row := range rows {
		out = append(out, "  "+row)
	}
	return out, true
}

func (r *Renderer) helperNotice(it *transcript.Item) []string {
	first, text, _ := strings.Cut(it.Text, "\n")
	kind := "report"
	if strings.Contains(first, "needs an answer") {
		kind = "question"
	}
	if strings.Contains(first, "has stopped") {
		kind = "stopped"
		text = first
	}
	head := fmt.Sprintf("%s helper %d %s %s %s %s", r.th.G.Wire, it.Helper, r.th.G.Arrow, r.clean(r.o.Contact), r.th.G.Mid, kind)
	rows := r.indented(toneTool, r.th.Wrap(r.clean(text), r.inner))
	out := make([]string, 0, len(rows)+1)
	out = append(out, r.tone(toneTool, head))
	return append(out, rows...)
}

// HelperRow renders a safe, width-bounded summary, shortening the title first.
func (r *Renderer) HelperRow(h helperboard.Helper, now int64, width int) string {
	state := strings.TrimPrefix(h.State, "TASK_STATE_")
	tone := "dim"
	switch state {
	case "WORKING":
		tone = "ok"
	case "INPUT_REQUIRED":
		tone = "warn"
	case "FAILED":
		tone = "bad"
	}
	detail := state
	if h.NextCheck > 0 {
		seconds := max(0, (h.NextCheck-now)/1000)
		next := fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
		if seconds >= 3600 {
			loc := r.o.Location
			if loc == nil {
				loc = time.Local
			}
			next = time.UnixMilli(h.NextCheck).In(loc).Format("15:04")
		}
		detail += " " + r.th.G.Mid + " next " + next
	}
	detail += fmt.Sprintf(" %s %d checks %s %d reports", r.th.G.Mid, h.Checks, r.th.G.Mid, h.Reports)
	if h.LastOutcome != "" && h.LastCheck > 0 {
		detail += fmt.Sprintf(" %s %s %ds ago", r.th.G.Mid, r.clean(h.LastOutcome), max(0, (now-h.LastCheck)/1000))
	}
	prefix := fmt.Sprintf("%s helper %d  ", r.th.G.Dot, h.Handle)
	title := r.th.Truncate(r.clean(h.Title), max(0, width-r.th.Width(prefix+detail)-3))
	return r.tone(tone, r.th.Truncate(prefix+title+"  "+detail, max(width, 0)))
}
