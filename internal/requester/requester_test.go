package requester_test

import (
	"context"
	"testing"
	"time"

	"github.com/quinnovator/contactd/internal/api/apitest"
	"github.com/quinnovator/contactd/internal/client"
	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/helpers"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/requester"
	"github.com/quinnovator/contactd/internal/turn"
)

type observer struct{}

func (observer) Observe(context.Context, core.ContactID, core.HelperDelta) error { return nil }
func (observer) HelperNotice(context.Context, core.ContactID, core.NoticeBody) (uint64, error) {
	return 1, nil
}

func TestRequesterOwnershipAndCap(t *testing.T) {
	t.Parallel()
	cfg := helpers.DefaultConfig()
	cfg.Auth = "off"
	s := apitest.Start(t, apitest.Options{Helpers: &cfg})
	c := newClient(t, s)
	parent, err := c.Create(t.Context(), client.NewContact{Name: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	r := requester.New(s.Store, observer{}, requester.Config{AgentURL: s.URL, BaseURL: s.URL, PerContact: 1}, obs.Discard())
	h, err := r.Start(t.Context(), parent, turn.HelperStart{Title: "Watch", Brief: "Read once", Every: time.Minute, For: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if h.Every != time.Minute || h.Handle == 0 {
		t.Fatalf("start: %+v", h)
	}
	if _, err := r.Start(t.Context(), parent, turn.HelperStart{Title: "Second", Brief: "Read again"}); err == nil {
		t.Fatal("cap not enforced")
	}
	if _, err := r.Message(t.Context(), parent+1, h.Handle, "hello"); err == nil {
		t.Fatal("foreign handle allowed")
	}
	if _, err := r.Stop(t.Context(), parent+1, h.Handle); err == nil {
		t.Fatal("foreign cancellation allowed")
	}
	if _, err := r.Message(t.Context(), parent, h.Handle, "new brief"); err != nil {
		t.Fatal(err)
	}
	if hs, err := r.List(t.Context(), parent); err != nil || len(hs) != 1 {
		t.Fatalf("list: %+v %v", hs, err)
	}
	if _, err := r.Stop(t.Context(), parent, h.Handle); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Message(t.Context(), parent, h.Handle, "too late"); err == nil {
		t.Fatal("terminal message accepted")
	}
	if _, err := r.Stop(t.Context(), parent, h.Handle); err != nil {
		t.Fatalf("already stopped: %v", err)
	}
}
