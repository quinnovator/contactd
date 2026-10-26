package requester

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/turn"
)

func instant(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func info(h core.RemoteHelper) turn.HelperInfo {
	return turn.HelperInfo{Handle: h.Handle, Title: h.Title, TaskID: h.TaskID, State: h.State, Every: time.Duration(h.EveryMS) * time.Millisecond, Until: instant(h.UntilTS), NextCheck: instant(h.NextCheck), Checks: h.Checks, Reports: h.Reports, Message: h.StatusMessage}
}

// List refreshes active tasks within a shared five second budget.
func (r *Requester) List(ctx context.Context, parent core.ContactID) ([]turn.HelperInfo, error) {
	rows, err := r.store.ListRemoteHelpers(ctx, parent, 20)
	if err != nil {
		return nil, fmt.Errorf("list helpers: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range rows {
		if a2a.TaskState(rows[i].State).Terminal() {
			continue
		}
		wg.Go(func() {
			call, cancel := callContext(ctx)
			defer cancel()
			c, err := r.connect(call)
			if err != nil {
				return
			}
			row := rows[i]
			req := &a2a.GetTaskRequest{ID: a2a.TaskID(row.TaskID)}
			r.observe(call, row, "call", "GetTask", "out", req, "Refreshing helper")
			task, err := c.GetTask(call, req)
			if err != nil {
				r.observe(call, row, "error", "GetTask", "in", nil, err.Error())
				return
			}
			updated, err := r.saveTask(call, row.Handle, task)
			if err == nil {
				rows[i] = updated
				r.observe(call, updated, "result", "GetTask", "in", task, updated.State)
			}
		})
	}
	wg.Wait()
	out := make([]turn.HelperInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, info(row))
	}
	return out, nil
}

// Helpers returns durable, credential-free snapshots without network polling.
func (r *Requester) Helpers(ctx context.Context, parent core.ContactID) ([]core.HelperJSON, error) {
	rows, err := r.store.ListRemoteHelpers(ctx, parent, 20)
	if err != nil {
		return nil, fmt.Errorf("list helper snapshots: %w", err)
	}
	out := make([]core.HelperJSON, 0, len(rows))
	for _, h := range rows {
		out = append(out, core.HelperJSON{Handle: h.Handle, Contact: h.HelperContact, Title: h.Title, TaskID: h.TaskID, ContextID: h.ContextID, State: h.State, StatusMessage: h.StatusMessage, EveryMS: h.EveryMS, Until: h.UntilTS, NextCheck: h.NextCheck, Checks: h.Checks, Reports: h.Reports, LastCheckAt: h.LastCheckAt, LastReportAt: h.LastReportAt, LastOutcome: h.LastOutcome, AgentURL: h.AgentURL})
	}
	return out, nil
}
