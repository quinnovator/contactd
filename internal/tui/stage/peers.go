package stage

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/tui/avatar"
	"github.com/quinnovator/contactd/internal/tui/canvas"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/speech"
)

const (
	peerLinger = 8 * time.Second
	packetTime = 900 * time.Millisecond
	maxPeers   = 16
)

// Exchange is an observed protocol event, never an inferred tool invocation.
// Direction is "in" or "out" relative to the main avatar. Payloads and message
// bodies are deliberately absent: the wire inspector owns those details.
type Exchange struct {
	Handle                                 uint64
	Title, Direction, Method, Event, State string
}

type packet struct {
	direction, label string
	until            time.Time
}

type peer struct {
	handle          uint64
	title           string
	state           string
	result          string
	last            time.Time
	avatar          *avatar.Avatar
	queue           []packet
	active          bool
	nextCheck       int64
	checks, reports int
}

type peerPlace struct {
	peer   *peer
	face   rect
	label  rect
	link   rect
	canvas *canvas.Canvas
	card   rect
}

// Observe briefly brings the communicating helper onto the stage. A bounded
// queue keeps a fast request/response pair visible in the right order, without
// replaying old traffic for minutes after a burst. Snapshots do not call this.
func (s *Stage) Observe(ex Exchange, now time.Time) {
	if ex.Handle == 0 || ex.Method == "" || (ex.Direction != "in" && ex.Direction != "out") {
		return
	}
	s.AdvancePeers(now)
	p := s.ensurePeer(ex.Handle)
	if ex.Title != "" {
		p.title = safe.Line(ex.Title, 160, false)
	}
	if ex.State != "" {
		p.state = ex.State
		if !ongoing(ex.State) {
			p.active = false
		}
	}
	p.last = now
	if !slices.ContainsFunc(s.plan.peers, func(placed peerPlace) bool { return placed.peer == p }) {
		s.stale = true
	}
	label := exchangeLabel(ex)
	p.result = label
	if n := len(p.queue); n > 0 && p.queue[n-1].direction == ex.Direction && p.queue[n-1].label == label {
		return // repeated status pushes need no additional animation
	}
	until := now.Add(packetTime)
	if len(p.queue) > 0 {
		until = p.queue[len(p.queue)-1].until.Add(packetTime)
	}
	if len(p.queue) == 4 {
		// Preserve the start of the exchange and its latest result.
		p.queue[3] = packet{ex.Direction, label, p.queue[3].until}
	} else {
		p.queue = append(p.queue, packet{ex.Direction, label, until})
	}
}

// AdvancePeers also runs when drawing after a pause, so old network activity
// cannot restart merely because the window becomes visible again.
func (s *Stage) AdvancePeers(now time.Time) {
	s.now = now
	for _, p := range s.peers {
		for len(p.queue) > 0 && !now.Before(p.queue[0].until) {
			p.queue = p.queue[1:]
		}
	}
	n := len(s.peers)
	s.peers = slices.DeleteFunc(s.peers, func(p *peer) bool { return !p.active && now.Sub(p.last) >= peerLinger })
	if n != len(s.peers) {
		s.stale = true
	}
}

// ClearPeers ends the visual exchange when its stream disconnects.
func (s *Stage) ClearPeers() {
	if len(s.peers) > 0 {
		s.peers, s.stale = nil, true
	}
}

func exchangeLabel(ex Exchange) string {
	if ex.Event == "error" {
		return "request failed"
	}
	if ex.Direction == "out" {
		switch ex.Method {
		case "SendMessage":
			return "message sent"
		case "CancelTask":
			return "stop requested"
		case "GetTask":
			return "status requested"
		default:
			return "request sent"
		}
	}
	switch ex.Method {
	case "artifactUpdate":
		return "report received"
	case "statusUpdate":
		return "status received"
	default:
		return "reply received"
	}
}

func (p *peer) status(now time.Time) string {
	if len(p.queue) > 0 {
		return p.queue[0].label
	}
	if p.result == "request failed" {
		return p.result
	}
	switch p.state {
	case "TASK_STATE_WORKING":
		if p.nextCheck > now.UnixMilli() {
			return "watching · scheduled"
		}
		return "working"
	case "TASK_STATE_INPUT_REQUIRED":
		return "needs an answer"
	case "TASK_STATE_COMPLETED":
		return "completed"
	case "TASK_STATE_FAILED", "TASK_STATE_REJECTED":
		return "failed"
	case "TASK_STATE_CANCELED":
		return "stopped"
	case "TASK_STATE_SUBMITTED":
		return "queued"
	case "TASK_STATE_AUTH_REQUIRED":
		return "needs authentication"
	default:
		return p.result
	}
}

func (s *Stage) packetMouth() speech.Viseme {
	return speech.Viseme{Open: 0.15 + 0.55*math.Abs(math.Sin(s.elapsed*11)), Spread: 0.4}
}

func (s *Stage) stepPeers() bool {
	sending := false
	for _, placed := range s.plan.peers {
		p := placed.peer
		mood, mouth := avatar.Idle, speech.Rest
		if len(p.queue) > 0 {
			p.avatar.LookAt(-1, 0)
			s.avatar.LookAt(1, 0)
			if p.queue[0].direction == "in" {
				mood, mouth = avatar.Speaking, s.packetMouth()
			} else {
				sending = true
			}
		} else if p.state == "TASK_STATE_COMPLETED" {
			mood = avatar.Content
		} else if p.active && p.state == "TASK_STATE_WORKING" && p.nextCheck <= s.now.UnixMilli() {
			mood = avatar.Thinking
		}
		p.avatar.Step(mood, mouth, 0)
	}
	return sending
}

func (s *Stage) placePeers(plan *plan) {
	a := plan.face
	if len(s.peers) == 0 || a.w < 54 || a.h < 11 {
		return
	}
	selected := slices.Clone(s.peers)
	// Give recently communicating helpers a place, then retain identity order.
	slices.SortStableFunc(selected, func(a, b *peer) int { return b.last.Compare(a.last) })
	count := min(len(selected), 3, a.h/11)
	selected = selected[:count]
	slices.SortFunc(selected, func(a, b *peer) int { return cmp.Compare(a.handle, b.handle) })
	const peerWidth, linkWidth = 24, 6
	mainWidth := a.w - peerWidth - linkWidth
	mainHeight := min(a.h, mainWidth*2/5)
	plan.face = rect{a.x, a.y + (a.h-mainHeight)/2, mainWidth, mainHeight}
	slot := a.h / count
	for i, p := range selected {
		height := min(slot-5, 8)
		x, y := a.x+a.w-peerWidth, a.y+i*slot+(slot-height-5)/2
		plan.peers = append(plan.peers, peerPlace{
			peer: p, card: rect{x, y, peerWidth, height + 5},
			face:   rect{x + 1, y + 1, peerWidth - 2, height},
			label:  rect{x + 1, y + height + 1, peerWidth - 2, 3},
			link:   rect{x - linkWidth, y + 1 + height/2, linkWidth, 1},
			canvas: canvas.New(peerWidth-2, height, s.grid),
		})
	}
}

func (s *Stage) drawPeers(put func(int, piece)) {
	if len(s.peers) == 0 || s.plan.a2a.h == 0 {
		return
	}
	line := func(box rect, row int, text, tone string) {
		text = s.th.Truncate(text, box.w)
		width := s.th.Width(text)
		put(box.y+row, piece{x: box.x + (box.w-width)/2, w: width, s: s.th.Style(tone).Render(text)})
	}
	header := "A2A helpers · /a2a"
	if n := len(s.peers) - len(s.plan.peers); n > 0 {
		if len(s.plan.peers) == 0 {
			p := s.peers[0]
			for _, candidate := range s.peers[1:] {
				if candidate.last.After(p.last) {
					p = candidate
				}
			}
			direction := "·"
			if len(p.queue) > 0 {
				direction = "→"
				if p.queue[0].direction == "in" {
					direction = "←"
				}
			}
			header = fmt.Sprintf("A2A %s helper %d · %s", direction, p.handle, p.status(s.now))
			if n > 1 {
				header += fmt.Sprintf(" · +%d", n-1)
			}
		} else {
			header = fmt.Sprintf("A2A · +%d more · /a2a", n)
		}
	}
	line(s.plan.a2a, 0, header, "dim")
	for _, placed := range s.plan.peers {
		p := placed.peer
		border := s.th.Style("dim")
		title := fmt.Sprintf(" helper %d ", p.handle)
		title = s.th.Truncate(title, placed.card.w-2)
		top := "╭" + title + strings.Repeat("─", max(placed.card.w-2-s.th.Width(title), 0)) + "╮"
		put(placed.card.y, piece{x: placed.card.x, w: placed.card.w, s: border.Render(top)})
		put(placed.card.y+placed.card.h-1, piece{x: placed.card.x, w: placed.card.w, s: border.Render("╰" + strings.Repeat("─", placed.card.w-2) + "╯")})
		for y := placed.card.y + 1; y < placed.card.y+placed.card.h-1; y++ {
			put(y, piece{x: placed.card.x, w: 1, s: border.Render("│")})
			put(y, piece{x: placed.card.x + placed.card.w - 1, w: 1, s: border.Render("│")})
		}
		p.avatar.Draw(placed.canvas)
		for i, row := range strings.Split(placed.canvas.Render(s.paper), "\n") {
			put(placed.face.y+i, piece{x: placed.face.x, w: placed.face.w, s: closed(row)})
		}
		line(placed.label, 0, p.title, "accent")
		tone := "dim"
		status := p.status(s.now)
		if status == "request failed" || status == "failed" || status == "needs an answer" || status == "needs authentication" {
			tone = "warn"
		}
		line(placed.label, 1, status, tone)
		line(placed.label, 2, p.detail(s.now), "dim")
		if len(p.queue) > 0 {
			// The arrow travels from sender to receiver, never while idle.
			age := packetTime - p.queue[0].until.Sub(s.now)
			pos := min(max(int(age*4/packetTime), 0), 3)
			arrow := '→'
			if p.queue[0].direction == "in" {
				pos, arrow = 3-pos, '←'
			}
			dots := []rune("····")
			dots[pos] = arrow
			line(placed.link, 0, string(dots), "accent")
		}
	}
}
