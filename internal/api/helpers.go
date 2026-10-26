package api

import (
	"context"
	"net/http"

	"github.com/quinnovator/contactd/internal/core"
)

// Helpers lists durable helper snapshots; implemented by internal/requester.
type Helpers interface {
	Helpers(context.Context, core.ContactID) ([]core.HelperJSON, error)
}

// WithHelpers enables helper listings. Call before serving.
func (s *Server) WithHelpers(h Helpers) *Server { s.helpers = h; return s }

func (s *Server) getHelpers(w http.ResponseWriter, r *http.Request) {
	id, ok := s.contactID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.Contact(r.Context(), id); err != nil {
		s.fail(w, r, statusOf(err), err)
		return
	}
	rows := []core.HelperJSON{}
	if s.helpers != nil {
		var err error
		rows, err = s.helpers.Helpers(r.Context(), id)
		if err != nil {
			s.fail(w, r, statusOf(err), err)
			return
		}
		if rows == nil {
			rows = []core.HelperJSON{}
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Helpers []core.HelperJSON `json:"helpers"`
	}{rows})
}
