package client_test

import (
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/api/apitest"
	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
)

func TestFollowHelperIsPassive(t *testing.T) {
	t.Parallel()
	srv := apitest.Start(t, apitest.Options{})
	id, err := srv.Store.CreateContact(t.Context(), core.AgentConfig{Helper: true}, "", time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	c := overTCP(t, srv)
	sess := c.Follow(t.Context(), id)
	defer sess.Close()
	snapshots := 0
	waitFor(t, sess, nil, func(u client.Update) bool {
		if u.Kind == client.UpdateHelpers {
			snapshots++
		}
		return isLive(u)
	})
	if snapshots != 1 {
		t.Fatalf("snapshots %d", snapshots)
	}
	events, err := srv.Store.EventsAfterOfKinds(t.Context(), id, 0, 100, core.KindSet(0).With(core.KindNotice))
	if err != nil || len(events) != 0 {
		t.Fatalf("passive follow wrote notices: %+v %v", events, err)
	}
}
