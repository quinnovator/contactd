package helpers

import (
	"context"

	"github.com/quinnovator/contactd/internal/core"
)

// Contacts is the part of the actor registry helpers drive. *actor.Registry implements it.
type Contacts interface {
	MessageFrom(ctx context.Context, id core.ContactID, text, raw, messageID string) (uint64, error)
	Check(ctx context.Context, id core.ContactID, text string) (uint64, error)
}

// Creator makes helper contacts. *eventlog.Store implements it.
type Creator interface {
	CreateContact(ctx context.Context, agent core.AgentConfig, profile string, now int64) (core.ContactID, error)
}

// Events reads a helper contact's log. *eventlog.Store implements it.
type Events interface {
	EventsAfterOfKinds(ctx context.Context, id core.ContactID, after uint64, limit int, kinds core.KindSet) ([]core.Event, error)
	EventsBefore(ctx context.Context, id core.ContactID, before uint64, limit int, kinds core.KindSet) ([]core.Event, error)
}

// Store keeps helper tasks and their push configs. *eventlog.Store implements it.
type Store interface {
	InsertHelperTask(ctx context.Context, t core.HelperTask) error
	HelperTask(ctx context.Context, id string) (core.HelperTask, error) // core.ErrTaskNotFound
	HelperTaskByContact(ctx context.Context, contact core.ContactID) (core.HelperTask, error)
	HelperTaskByMessageID(ctx context.Context, messageID string) (core.HelperTask, error) // the brief's; core.ErrTaskNotFound
	UpdateHelperTask(ctx context.Context, t core.HelperTask) error                        // writes every mutable column
	DueHelperTasks(ctx context.Context, now int64, limit int) ([]core.HelperTask, error)
	NextHelperDue(ctx context.Context) (int64, error)                  // 0 when none
	PendingHelperTasks(ctx context.Context) ([]core.HelperTask, error) // not terminal, or pushed_state != state
	ListHelperTasks(ctx context.Context, f core.HelperTaskFilter) ([]core.HelperTask, error)
	CountHelperTasks(ctx context.Context, f core.HelperTaskFilter) (int, error)
	CountActiveHelperTasks(ctx context.Context) (int, error)
	PutPushConfig(ctx context.Context, c core.PushConfig) error
	PushConfig(ctx context.Context, taskID, id string) (core.PushConfig, error)
	PushConfigs(ctx context.Context, taskID string) ([]core.PushConfig, error)
	DeletePushConfig(ctx context.Context, taskID, id string) error
}

// Deps are the durable service adapters.
type Deps struct {
	Contacts Contacts
	Creator  Creator
	Events   Events
	Store    Store
}
