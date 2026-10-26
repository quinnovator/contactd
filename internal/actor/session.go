package actor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/core"
)

// beginVisit runs only for an accepted human message, after interrupted replies
// and settlements have been logged. Opening a connection alone changes nothing.
func (a *actor) beginVisit(ctx context.Context, v core.Visit, now time.Time) {
	if a.contact.Agent.Unattended() {
		return
	}
	if v.TimeZone != "" {
		if loc, err := time.LoadLocation(v.TimeZone); err == nil {
			a.location = loc
		}
	}
	fresh := a.lastSpoke == 0 || now.Sub(time.UnixMilli(a.lastSpoke)) >= 30*time.Minute
	if v.SessionID != "" {
		_, seen := a.sessions[v.SessionID]
		fresh = !seen
	}
	if !fresh {
		return
	}
	local := now.In(a.location)
	var b strings.Builder
	if a.lastSpoke == 0 {
		b.WriteString("The person has arrived; this is your first meeting.\n")
	} else {
		b.WriteString("The person has just come back; this is a new session.\n")
	}
	fmt.Fprintf(&b, "Their local time: %s (%s).\n", local.Format("Monday 2 Jan 2006, 15:04 -07:00"), a.location)
	if a.lastSpoke != 0 {
		last := time.UnixMilli(a.lastSpoke).In(a.location)
		fmt.Fprintf(&b, "You last spoke %s ago, on %s %s.\n", elapsed(now.Sub(last)), last.Format("Monday 2 Jan 2006"), dayPart(last.Hour()))
	}
	switch v.Presentation {
	case "avatar":
		b.WriteString("They see you as an avatar that speaks your replies aloud. " +
			"Markdown tables, code blocks and images are displayed beside you. " +
			"The previous reply's display stays visible until you provide a new one; prose alone does not edit it. " +
			"When reporting changed values from a table you displayed, including changes reported by a helper, " +
			"include the complete updated Markdown table in your reply so the display is replaced. " +
			"Use only known values; mark unknown or unconfirmed values explicitly.")
	case "text":
		b.WriteString("They see your replies as text.")
	}
	a.append(ctx, core.KindNotice, core.NoticeBody{Source: "session-start", SessionID: v.SessionID, Text: strings.TrimSpace(b.String())}, nil)
	if v.SessionID != "" {
		a.sessions[v.SessionID] = struct{}{}
	}
}

func elapsed(d time.Duration) string {
	minutes := max(int(d/time.Minute), 0)
	if minutes < 1 {
		return "less than a minute"
	}
	days, hours := minutes/(24*60), minutes/60%24
	if days > 0 {
		return fmt.Sprintf("%d days %d hours", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%d hours %d minutes", hours, minutes%60)
	}
	return fmt.Sprintf("%d minutes", minutes)
}

func dayPart(hour int) string {
	switch {
	case hour < 5:
		return "at night"
	case hour < 12:
		return "in the morning"
	case hour < 17:
		return "in the afternoon"
	case hour < 21:
		return "in the evening"
	default:
		return "at night"
	}
}
