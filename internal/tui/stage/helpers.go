package stage

import (
	"fmt"
	"slices"
	"time"

	"github.com/quinnovator/contactd/internal/tui/avatar"
	"github.com/quinnovator/contactd/internal/tui/safe"
)

// Helper is current background task state. Unlike Exchange, it carries no
// implication that a message is travelling or that the helper is speaking.
type Helper struct {
	Handle          uint64
	Title, State    string
	NextCheck       int64
	Checks, Reports int
}

// SyncHelpers keeps unfinished tasks on stage across turns and quiet periods.
// It accepts an authoritative snapshot; missing tasks leave, and newly seen
// terminal history stays off stage. Repeated snapshots never restart a finish
// animation or extend its lifetime.
func (s *Stage) SyncHelpers(helpers []Helper, now time.Time) {
	s.AdvancePeers(now)
	wanted := make(map[uint64]bool, len(helpers))
	for _, h := range helpers {
		wanted[h.Handle] = true
	}
	n := len(s.peers)
	s.peers = slices.DeleteFunc(s.peers, func(p *peer) bool { return p.active && !wanted[p.handle] })
	if len(s.peers) != n {
		s.stale = true
	}
	added := 0
	for _, h := range helpers {
		if h.Handle == 0 {
			continue
		}
		p := s.findPeer(h.Handle)
		active := ongoing(h.State)
		if p == nil && (!active || added >= maxPeers) {
			continue
		}
		if active {
			added++
		}
		if p == nil {
			p = s.ensurePeer(h.Handle)
		}
		if p.active && !active {
			p.last = now
		}
		if p.state != h.State || p.checks < h.Checks || p.reports < h.Reports {
			p.result = ""
		}
		p.title = safe.Line(h.Title, 160, false)
		p.state, p.active = h.State, active
		p.nextCheck, p.checks, p.reports = h.NextCheck, h.Checks, h.Reports
	}
}

func ongoing(state string) bool {
	switch state {
	case "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED":
		return true
	}
	return false
}

func (s *Stage) findPeer(handle uint64) *peer {
	for _, p := range s.peers {
		if p.handle == handle {
			return p
		}
	}
	return nil
}

func (s *Stage) ensurePeer(handle uint64) *peer {
	if p := s.findPeer(handle); p != nil {
		return p
	}
	if len(s.peers) == maxPeers {
		oldest := 0
		for i, p := range s.peers {
			previous := s.peers[oldest]
			if (previous.active && !p.active) || (previous.active == p.active && p.last.Before(previous.last)) {
				oldest = i
			}
		}
		s.peers = slices.Delete(s.peers, oldest, oldest+1)
	}
	looks := avatar.Palettes()
	p := &peer{handle: handle, avatar: avatar.New(looks[1+int((handle-1)%3)], handle)}
	s.peers = append(s.peers, p)
	s.stale = true
	return p
}

func (p *peer) detail(now time.Time) string {
	if p.active && p.nextCheck > now.UnixMilli() {
		seconds := max((p.nextCheck-now.UnixMilli()+999)/1000, 0)
		return fmt.Sprintf("next check %d:%02d", seconds/60, seconds%60)
	}
	checks, reports := "checks", "reports"
	if p.checks == 1 {
		checks = "check"
	}
	if p.reports == 1 {
		reports = "report"
	}
	return fmt.Sprintf("%d %s · %d %s", p.checks, checks, p.reports, reports)
}
