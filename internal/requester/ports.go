// Package requester starts remote A2A tasks and turns authenticated pushes into
// durable, non-interrupting notices. Protocol calls always traverse HTTP.
package requester

import (
	"context"

	"github.com/quinnovator/contactd/internal/core"
)

// Store persists requester state. *eventlog.Store implements it.
type Store interface {
	InsertRemoteHelper(context.Context, core.RemoteHelper) (uint64, error)
	RemoteHelper(context.Context, uint64) (core.RemoteHelper, error)
	RemoteHelperByToken(context.Context, string) (core.RemoteHelper, error)
	RemoteHelperByTask(context.Context, string) (core.RemoteHelper, error)
	ListRemoteHelpers(context.Context, core.ContactID, int) ([]core.RemoteHelper, error)
	UpdateRemoteHelper(context.Context, core.RemoteHelper) error
	DeleteRemoteHelper(context.Context, uint64) error
	CountActiveRemoteHelpers(context.Context, core.ContactID) (int, error)
	SeenArtifact(context.Context, uint64, string) (bool, error)
	MarkArtifactSeen(context.Context, uint64, string) error
}

// Registry delivers notices and ephemeral diagnostics. *actor.Registry implements it.
type Registry interface {
	HelperNotice(context.Context, core.ContactID, core.NoticeBody) (uint64, error)
	Observe(context.Context, core.ContactID, core.HelperDelta) error
}
