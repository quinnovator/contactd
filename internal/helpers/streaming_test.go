package helpers

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

func TestBrokerOrderedBroadcastAndSlowSubscriber(t *testing.T) {
	t.Parallel()
	var b broker
	one, cancelOne := b.subscribe("t")
	defer cancelOne()
	two, cancelTwo := b.subscribe("t")
	defer cancelTwo()
	for i := range 3 {
		ev := a2a.NewStatusUpdateEvent(a2a.TaskInfo{TaskID: "t"}, a2a.TaskStateWorking, nil)
		b.publish(ev)
		if a, c := <-one.events, <-two.events; a != ev || c != ev {
			t.Fatalf("event %d out of order", i)
		}
	}
	end := a2a.NewStatusUpdateEvent(a2a.TaskInfo{TaskID: "t"}, a2a.TaskStateCompleted, nil)
	b.publish(end)
	if <-one.events != end {
		t.Fatal("lost terminal event")
	}
	if _, ok := <-one.events; ok {
		t.Fatal("terminal stream stayed open")
	}
	slow, cancelSlow := b.subscribe("slow")
	defer cancelSlow()
	for range 65 {
		b.publish(a2a.NewStatusUpdateEvent(a2a.TaskInfo{TaskID: "slow"}, a2a.TaskStateWorking, nil))
	}
	var got error
	for _, err := range b.stream(t.Context(), slow, cancelSlow) {
		if err != nil {
			got = err
		}
	}
	if got == nil {
		t.Fatal("slow subscriber ended without error")
	}
}

func TestSDKStreamsAndBlockingSend(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	handler, err := s.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	cfg := s.cfg
	cfg.BaseURL = server.URL
	c, err := a2aclient.NewFromCard(t.Context(), Card(cfg))
	if err != nil {
		t.Fatal(err)
	}
	first := true
	var id a2a.TaskID
	for ev, err := range c.SendStreamingMessage(t.Context(), sendRequest("watch")) {
		if err != nil {
			t.Fatal(err)
		}
		if first {
			task, ok := ev.(*a2a.Task)
			if !ok {
				t.Fatalf("first event %T", ev)
			}
			id = task.ID
			first = false
			if _, err := s.Cancel(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			if err := s.deliver(t.Context(), string(id), &delivery{}); err != nil {
				t.Fatal(err)
			}
		} else {
			e, ok := ev.(*a2a.TaskStatusUpdateEvent)
			if !ok || e.Status.State != a2a.TaskStateCanceled {
				t.Fatalf("terminal: %+v", ev)
			}
		}
	}
	if first {
		t.Fatal("empty stream")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := sendRequest("blocking")
	r.Config.ReturnImmediately = false
	if _, err := c.SendMessage(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled send: %v", err)
	}
	task := startTask(t, s)
	for ev, err := range c.SubscribeToTask(t.Context(), &a2a.SubscribeToTaskRequest{ID: task.ID}) {
		if err != nil {
			t.Fatal(err)
		}
		if snap, ok := ev.(*a2a.Task); !ok || snap.ID != task.ID {
			t.Fatalf("subscription starts with %T", ev)
		}
		break
	}
}
