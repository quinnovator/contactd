package eventlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog/sqlcgen"
)

// helperError preserves the storage sentinel at the adapter boundary.
func helperError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, core.ErrTaskNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func helperTaskFromRow(r sqlcgen.HelperTask) core.HelperTask {
	return core.HelperTask{
		ID:            r.ID,
		ContextID:     r.ContextID,
		Contact:       core.ContactID(u64(r.Contact)),
		Title:         r.Title,
		State:         r.State,
		StatusMessage: r.StatusMessage,
		StatusTS:      r.StatusTs,
		EveryMS:       r.EveryMs,
		UntilTS:       r.UntilTs,
		MaxChecks:     int(r.MaxChecks),
		NextDue:       r.NextDue,
		Checks:        int(r.Checks),
		Failures:      int(r.Failures),
		LastCheckAt:   r.LastCheckAt,
		LastOutcome:   r.LastOutcome,
		FirstSeq:      u64(r.FirstSeq),
		PushedSeq:     u64(r.PushedSeq),
		PushedState:   r.PushedState,
		CreatedAt:     r.CreatedAt,
	}
}

// InsertHelperTask persists a HelperTask row.
func (s *Store) InsertHelperTask(ctx context.Context, v core.HelperTask) error {
	err := sqlcgen.New(s.wdb).InsertHelperTask(ctx, sqlcgen.InsertHelperTaskParams{
		ID:            v.ID,
		ContextID:     v.ContextID,
		Contact:       i64(uint64(v.Contact)),
		Title:         v.Title,
		State:         v.State,
		StatusMessage: v.StatusMessage,
		StatusTs:      v.StatusTS,
		EveryMs:       v.EveryMS,
		UntilTs:       v.UntilTS,
		MaxChecks:     int64(v.MaxChecks),
		NextDue:       v.NextDue,
		Checks:        int64(v.Checks),
		Failures:      int64(v.Failures),
		LastCheckAt:   v.LastCheckAt,
		LastOutcome:   v.LastOutcome,
		FirstSeq:      i64(v.FirstSeq),
		PushedSeq:     i64(v.PushedSeq),
		PushedState:   v.PushedState,
		CreatedAt:     v.CreatedAt,
	})
	return helperError("InsertHelperTask", err)
}

// UpdateHelperTask persists a HelperTask row.
func (s *Store) UpdateHelperTask(ctx context.Context, v core.HelperTask) error {
	err := sqlcgen.New(s.wdb).UpdateHelperTask(ctx, sqlcgen.UpdateHelperTaskParams{
		ID:            v.ID,
		Title:         v.Title,
		State:         v.State,
		StatusMessage: v.StatusMessage,
		StatusTs:      v.StatusTS,
		EveryMs:       v.EveryMS,
		UntilTs:       v.UntilTS,
		MaxChecks:     int64(v.MaxChecks),
		NextDue:       v.NextDue,
		Checks:        int64(v.Checks),
		Failures:      int64(v.Failures),
		LastCheckAt:   v.LastCheckAt,
		LastOutcome:   v.LastOutcome,
		FirstSeq:      i64(v.FirstSeq),
		PushedSeq:     i64(v.PushedSeq),
		PushedState:   v.PushedState,
	})
	return helperError("UpdateHelperTask", err)
}

func pushConfigFromRow(r sqlcgen.HelperPushConfig) core.PushConfig {
	return core.PushConfig{
		TaskID:          r.TaskID,
		ID:              r.ID,
		URL:             r.Url,
		Token:           r.Token,
		AuthScheme:      r.AuthScheme,
		AuthCredentials: r.AuthCredentials,
		CreatedAt:       r.CreatedAt,
	}
}

// PutPushConfig persists a PushConfig row.
func (s *Store) PutPushConfig(ctx context.Context, v core.PushConfig) error {
	err := sqlcgen.New(s.wdb).PutPushConfig(ctx, sqlcgen.PutPushConfigParams{
		TaskID:          v.TaskID,
		ID:              v.ID,
		Url:             v.URL,
		Token:           v.Token,
		AuthScheme:      v.AuthScheme,
		AuthCredentials: v.AuthCredentials,
		CreatedAt:       v.CreatedAt,
	})
	return helperError("PutPushConfig", err)
}

func remoteHelperFromRow(r sqlcgen.RemoteHelper) core.RemoteHelper {
	return core.RemoteHelper{
		Handle:          u64(r.Handle),
		Parent:          core.ContactID(u64(r.Parent)),
		AgentURL:        r.AgentUrl,
		TaskID:          r.TaskID,
		ContextID:       r.ContextID,
		Title:           r.Title,
		State:           r.State,
		StatusMessage:   r.StatusMessage,
		HelperContact:   core.ContactID(u64(r.HelperContact)),
		EveryMS:         r.EveryMs,
		UntilTS:         r.UntilTs,
		NextCheck:       r.NextCheck,
		Checks:          int(r.Checks),
		Reports:         int(r.Reports),
		LastCheckAt:     r.LastCheckAt,
		LastReportAt:    r.LastReportAt,
		LastOutcome:     r.LastOutcome,
		PushToken:       r.PushToken,
		PushSecret:      r.PushSecret,
		CancelRequested: r.CancelRequested != 0,
		NotifiedState:   r.NotifiedState,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}

// InsertRemoteHelper persists a RemoteHelper row.
func (s *Store) InsertRemoteHelper(ctx context.Context, v core.RemoteHelper) (uint64, error) {
	n, err := sqlcgen.New(s.wdb).InsertRemoteHelper(ctx, sqlcgen.InsertRemoteHelperParams{
		Parent:          i64(uint64(v.Parent)),
		AgentUrl:        v.AgentURL,
		TaskID:          v.TaskID,
		ContextID:       v.ContextID,
		Title:           v.Title,
		State:           v.State,
		StatusMessage:   v.StatusMessage,
		HelperContact:   i64(uint64(v.HelperContact)),
		EveryMs:         v.EveryMS,
		UntilTs:         v.UntilTS,
		NextCheck:       v.NextCheck,
		Checks:          int64(v.Checks),
		Reports:         int64(v.Reports),
		LastCheckAt:     v.LastCheckAt,
		LastReportAt:    v.LastReportAt,
		LastOutcome:     v.LastOutcome,
		PushToken:       v.PushToken,
		PushSecret:      v.PushSecret,
		CancelRequested: boolInt(v.CancelRequested),
		NotifiedState:   v.NotifiedState,
		CreatedAt:       v.CreatedAt,
		UpdatedAt:       v.UpdatedAt,
	})
	return u64(n), helperError("InsertRemoteHelper", err)
}

// UpdateRemoteHelper persists a RemoteHelper row.
func (s *Store) UpdateRemoteHelper(ctx context.Context, v core.RemoteHelper) error {
	err := sqlcgen.New(s.wdb).UpdateRemoteHelper(ctx, sqlcgen.UpdateRemoteHelperParams{
		Handle:          i64(v.Handle),
		TaskID:          v.TaskID,
		ContextID:       v.ContextID,
		Title:           v.Title,
		State:           v.State,
		StatusMessage:   v.StatusMessage,
		HelperContact:   i64(uint64(v.HelperContact)),
		EveryMs:         v.EveryMS,
		UntilTs:         v.UntilTS,
		NextCheck:       v.NextCheck,
		Checks:          int64(v.Checks),
		Reports:         int64(v.Reports),
		LastCheckAt:     v.LastCheckAt,
		LastReportAt:    v.LastReportAt,
		LastOutcome:     v.LastOutcome,
		CancelRequested: boolInt(v.CancelRequested),
		NotifiedState:   v.NotifiedState,
		UpdatedAt:       v.UpdatedAt,
	})
	return helperError("UpdateRemoteHelper", err)
}

// HelperTask loads a durable helper record.
func (s *Store) HelperTask(ctx context.Context, id string) (core.HelperTask, error) {
	r, err := s.rq.GetHelperTask(ctx, id)
	return helperTaskFromRow(r), helperError("HelperTask", err)
}

// HelperTaskByContact loads a durable helper record.
func (s *Store) HelperTaskByContact(ctx context.Context, contact core.ContactID) (core.HelperTask, error) {
	r, err := s.rq.GetHelperTaskByContact(ctx, i64(uint64(contact)))
	return helperTaskFromRow(r), helperError("HelperTaskByContact", err)
}

// PushConfig loads a durable helper record.
func (s *Store) PushConfig(ctx context.Context, taskID, id string) (core.PushConfig, error) {
	r, err := s.rq.GetPushConfig(ctx, sqlcgen.GetPushConfigParams{TaskID: taskID, ID: id})
	return pushConfigFromRow(r), helperError("PushConfig", err)
}

// RemoteHelper loads a durable helper record.
func (s *Store) RemoteHelper(ctx context.Context, handle uint64) (core.RemoteHelper, error) {
	r, err := s.rq.GetRemoteHelper(ctx, i64(handle))
	return remoteHelperFromRow(r), helperError("RemoteHelper", err)
}

// RemoteHelperByToken loads a durable helper record.
func (s *Store) RemoteHelperByToken(ctx context.Context, token string) (core.RemoteHelper, error) {
	r, err := s.rq.GetRemoteHelperByToken(ctx, token)
	return remoteHelperFromRow(r), helperError("RemoteHelperByToken", err)
}

// RemoteHelperByTask loads a durable helper record.
func (s *Store) RemoteHelperByTask(ctx context.Context, taskID string) (core.RemoteHelper, error) {
	r, err := s.rq.GetRemoteHelperByTask(ctx, taskID)
	return remoteHelperFromRow(r), helperError("RemoteHelperByTask", err)
}

// DueHelperTasks reads matching helper records.
func (s *Store) DueHelperTasks(ctx context.Context, now int64, limit int) ([]core.HelperTask, error) {
	rows, err := s.rq.DueHelperTasks(ctx, sqlcgen.DueHelperTasksParams{Now: now, Page: int64(limit)})
	if err != nil {
		return nil, helperError("DueHelperTasks", err)
	}
	out := make([]core.HelperTask, len(rows))
	for i, r := range rows {
		out[i] = helperTaskFromRow(r)
	}
	return out, nil
}

// PendingHelperTasks reads matching helper records.
func (s *Store) PendingHelperTasks(ctx context.Context) ([]core.HelperTask, error) {
	rows, err := s.rq.PendingHelperTasks(ctx)
	if err != nil {
		return nil, helperError("PendingHelperTasks", err)
	}
	out := make([]core.HelperTask, len(rows))
	for i, r := range rows {
		out[i] = helperTaskFromRow(r)
	}
	return out, nil
}

// PushConfigs reads matching helper records.
func (s *Store) PushConfigs(ctx context.Context, taskID string) ([]core.PushConfig, error) {
	rows, err := s.rq.ListPushConfigs(ctx, taskID)
	if err != nil {
		return nil, helperError("PushConfigs", err)
	}
	out := make([]core.PushConfig, len(rows))
	for i, r := range rows {
		out[i] = pushConfigFromRow(r)
	}
	return out, nil
}

// ListRemoteHelpers reads matching helper records.
func (s *Store) ListRemoteHelpers(ctx context.Context, parent core.ContactID, limit int) ([]core.RemoteHelper, error) {
	rows, err := s.rq.ListRemoteHelpers(ctx, sqlcgen.ListRemoteHelpersParams{Parent: i64(uint64(parent)), Limit: int64(limit)})
	if err != nil {
		return nil, helperError("ListRemoteHelpers", err)
	}
	out := make([]core.RemoteHelper, len(rows))
	for i, r := range rows {
		out[i] = remoteHelperFromRow(r)
	}
	return out, nil
}

// CountActiveHelperTasks reads helper bookkeeping.
func (s *Store) CountActiveHelperTasks(ctx context.Context) (int, error) {
	n, err := s.rq.CountActiveHelperTasks(ctx)
	return int(n), helperError("CountActiveHelperTasks", err)
}

// NextHelperDue reads helper bookkeeping.
func (s *Store) NextHelperDue(ctx context.Context) (int64, error) {
	n, err := s.rq.NextHelperDue(ctx)
	return n, helperError("NextHelperDue", err)
}

// CountActiveRemoteHelpers reads helper bookkeeping.
func (s *Store) CountActiveRemoteHelpers(ctx context.Context, parent core.ContactID) (int, error) {
	n, err := s.rq.CountActiveRemoteHelpers(ctx, i64(uint64(parent)))
	return int(n), helperError("CountActiveRemoteHelpers", err)
}

// SeenArtifact reads helper bookkeeping.
func (s *Store) SeenArtifact(ctx context.Context, handle uint64, artifactID string) (bool, error) {
	n, err := s.rq.ArtifactSeen(ctx, sqlcgen.ArtifactSeenParams{Handle: i64(handle), ArtifactID: artifactID})
	return n != 0, helperError("SeenArtifact", err)
}

// DeletePushConfig updates helper bookkeeping idempotently.
func (s *Store) DeletePushConfig(ctx context.Context, taskID, id string) error {
	return helperError("DeletePushConfig", sqlcgen.New(s.wdb).DeletePushConfig(ctx, sqlcgen.DeletePushConfigParams{TaskID: taskID, ID: id}))
}

// DeleteRemoteHelper updates helper bookkeeping idempotently.
func (s *Store) DeleteRemoteHelper(ctx context.Context, handle uint64) error {
	return helperError("DeleteRemoteHelper", sqlcgen.New(s.wdb).DeleteRemoteHelper(ctx, i64(handle)))
}

// MarkArtifactSeen updates helper bookkeeping idempotently.
func (s *Store) MarkArtifactSeen(ctx context.Context, handle uint64, artifactID string) error {
	return helperError("MarkArtifactSeen", sqlcgen.New(s.wdb).MarkArtifactSeen(ctx, sqlcgen.MarkArtifactSeenParams{Handle: i64(handle), ArtifactID: artifactID}))
}

// ListHelperTasks returns a page ordered by creation time and task ID.
func (s *Store) ListHelperTasks(ctx context.Context, f core.HelperTaskFilter) ([]core.HelperTask, error) {
	var stamp int64
	var id string
	if f.Cursor != "" {
		var raw string
		raw, id, _ = strings.Cut(f.Cursor, ":")
		var err error
		stamp, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse helper cursor: %w", err)
		}
		if id == "" {
			return nil, errors.New("helper cursor has no task ID")
		}
	}
	rows, err := s.rq.ListHelperTasks(ctx, sqlcgen.ListHelperTasksParams{ContextID: f.ContextID, State: f.State, After: f.After, CursorID: id, CursorTs: stamp, Page: int64(f.Limit)})
	if err != nil {
		return nil, helperError("list helper tasks", err)
	}
	out := make([]core.HelperTask, len(rows))
	for i, r := range rows {
		out[i] = helperTaskFromRow(r)
	}
	return out, nil
}

// CountHelperTasks counts filtered tasks before pagination.
func (s *Store) CountHelperTasks(ctx context.Context, f core.HelperTaskFilter) (int, error) {
	n, err := s.rq.CountHelperTasks(ctx, sqlcgen.CountHelperTasksParams{ContextID: f.ContextID, State: f.State, After: f.After})
	return int(n), helperError("count helper tasks", err)
}
