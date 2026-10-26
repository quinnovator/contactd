package stage

import (
	"strings"
	"testing"
	"time"
)

func TestBackgroundCardsPersistWithoutInventingCommunication(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	now := time.Unix(100, 0)
	s.SyncHelpers([]Helper{{Handle: 1, Title: "Investigating", State: "TASK_STATE_WORKING", Checks: 3, Reports: 2}}, now)
	s.Perform(Turn{ID: 1, Text: "Meanwhile, the conversation continues.", Over: true}, true)
	s.View()
	face := s.plan.peers[0].card
	worker := s.peers[0].avatar
	for range 120 {
		s.step()
	}
	if worker.Pose().Focus == 0 || worker.Pose().Jaw > 0.001 {
		t.Fatal("a worker should think quietly, not talk")
	}
	s.AdvancePeers(now.Add(time.Hour))
	s.Perform(Turn{ID: 2, Text: "And another topic.", Over: true}, true)
	view := strings.Join(plain(s), "\n")
	if len(s.peers) != 1 || s.plan.peers[0].card != face || !strings.Contains(view, "3 checks · 2 reports") || strings.ContainsAny(view, "←→") {
		t.Fatalf("quiet background task vanished, moved, or invented traffic:\n%s", view)
	}
	s.SyncHelpers([]Helper{{Handle: 1, Title: "Investigating", State: "TASK_STATE_WORKING", Checks: 4, Reports: 2}}, now.Add(time.Hour))
	if s.peers[0].avatar != worker || s.stale {
		t.Fatal("a progress update should preserve identity and layout")
	}
}

func TestScheduledAndFinishedHelpersHaveDifferentLifetimes(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	now := time.Unix(100, 0)
	states := []Helper{
		{Handle: 1, Title: "Watch", State: "TASK_STATE_WORKING", NextCheck: now.Add(time.Minute).UnixMilli()},
		{Handle: 2, Title: "Old task", State: "TASK_STATE_COMPLETED"},
	}
	s.SyncHelpers(states, now)
	view := strings.Join(plain(s), "\n")
	if len(s.peers) != 1 || !strings.Contains(view, "scheduled") || !strings.Contains(view, "next check 1:00") {
		t.Fatalf("scheduled work or terminal history handled incorrectly:\n%s", view)
	}
	for range 120 {
		s.step()
	}
	if s.peers[0].avatar.Pose().Focus > 0.001 {
		t.Fatal("a scheduled helper should rest between checks")
	}
	states[0].State, states[0].NextCheck = "TASK_STATE_COMPLETED", 0
	s.SyncHelpers(states, now.Add(time.Second))
	s.SyncHelpers(states, now.Add(7*time.Second))
	if !strings.Contains(strings.Join(plain(s), "\n"), "completed") {
		t.Fatal("a newly completed helper should linger briefly")
	}
	s.SyncHelpers(states, now.Add(10*time.Second))
	if len(s.peers) != 0 {
		t.Fatal("repeated snapshots prolonged completion")
	}
}

func TestAuthoritativeSnapshotRemovesMissingBackgroundCards(t *testing.T) {
	t.Parallel()
	s := newStage(t, 160, 48, Options{})
	now := time.Unix(100, 0)
	s.SyncHelpers([]Helper{{Handle: 1, State: "TASK_STATE_WORKING"}}, now)
	s.SyncHelpers(nil, now.Add(time.Second))
	if len(s.peers) != 0 {
		t.Fatal("missing task remained active")
	}
}
