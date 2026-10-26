package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/quinnovator/contactd/internal/helpers"
	"github.com/quinnovator/contactd/internal/obs"
)

func TestScriptedTransports(t *testing.T) {
	t.Parallel()
	for _, binding := range []a2a.TransportProtocol{a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON} {
		t.Run(string(binding), func(t *testing.T) {
			t.Parallel()
			cfg := helpers.DefaultConfig()
			cfg.Auth = "off"
			h, err := helpers.HTTPHandler(newBackend(), cfg, obs.Discard())
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(h)
			defer server.Close()
			cfg.BaseURL = server.URL
			card := helpers.Card(cfg)
			if binding == a2a.TransportProtocolHTTPJSON {
				card.SupportedInterfaces = card.SupportedInterfaces[1:]
			} else {
				card.SupportedInterfaces[0].URL += "/"
			}
			client, err := a2aclient.NewFromCard(t.Context(), card)
			if err != nil {
				t.Fatal(err)
			}
			req := &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"))}
			req.Message.ID = "tck-message-response-test"
			result, err := client.SendMessage(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := result.(*a2a.Message); !ok {
				t.Fatalf("direct response is %T", result)
			}
			req.Message.ID = "tck-stream-artifact-chunked-test"
			var chunks []*a2a.TaskArtifactUpdateEvent
			var task *a2a.Task
			for ev, err := range client.SendStreamingMessage(t.Context(), req) {
				if err != nil {
					t.Fatal(err)
				}
				switch e := ev.(type) {
				case *a2a.Task:
					task = e
				case *a2a.TaskArtifactUpdateEvent:
					chunks = append(chunks, e)
				}
			}
			if task == nil || len(chunks) != 2 || chunks[0].LastChunk || !chunks[1].LastChunk || !chunks[1].Append || chunks[0].Artifact.ID != chunks[1].Artifact.ID {
				t.Fatalf("chunk lifecycle: %+v", chunks)
			}
			n := 1
			got, err := client.GetTask(t.Context(), &a2a.GetTaskRequest{ID: task.ID, HistoryLength: &n})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.History) != 1 || got.Status.State != a2a.TaskStateCompleted {
				t.Fatalf("snapshot: %+v", got)
			}
			if _, err := client.CancelTask(t.Context(), &a2a.CancelTaskRequest{ID: task.ID}); !errors.Is(err, a2a.ErrTaskNotCancelable) {
				t.Fatalf("terminal cancellation: %v", err)
			}
		})
	}
}

func TestDelayedTaskSurvivesDisconnect(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		b := newBackend()
		ctx, cancel := context.WithCancel(t.Context())
		req := &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("wait"))}
		req.Message.ID = "test-resubscribe-message-id"
		w, err := b.Send(ctx, req, true)
		if err != nil {
			t.Fatal(err)
		}
		id := w.Task.ID
		w.Close()
		cancel()
		again, err := b.Subscribe(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		defer again.Close()
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		events := 0
		for ev, err := range again.Events {
			if err != nil {
				t.Fatal(err)
			}
			events++
			if e, ok := ev.(*a2a.TaskStatusUpdateEvent); !ok || e.Status.State != a2a.TaskStateCompleted {
				t.Fatalf("completion: %+v", ev)
			}
		}
		if events != 1 {
			t.Fatalf("completion events=%d", events)
		}
	})
}
