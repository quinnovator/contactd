package eventlog_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/sql/schema"
)

func TestHelperStorage(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	ctx := t.Context()
	id, err := s.CreateContact(ctx, core.AgentConfig{Helper: true}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if cs, err := s.Contacts(ctx, 10); err != nil || len(cs) != 0 {
		t.Fatalf("helpers visible: %v %v", cs, err)
	}
	c, err := s.Contact(ctx, id)
	if err != nil || !c.Agent.Unattended() {
		t.Fatalf("helper config: %v %v", c, err)
	}
	state := string(a2a.TaskStateWorking)
	task := core.HelperTask{ID: "a", ContextID: "ctx", Contact: id, Title: "watch", State: state, StatusMessage: `{"messageId":"s"}`, StatusTS: 5, EveryMS: 10, UntilTS: 50, MaxChecks: 4, NextDue: 15, Checks: 1, Failures: 2, LastCheckAt: 4, LastOutcome: "failed", FirstSeq: 2, PushedSeq: 3, PushedState: state, CreatedAt: 1}
	if err := s.InsertHelperTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, got := range []func() (core.HelperTask, error){func() (core.HelperTask, error) { return s.HelperTask(ctx, "a") }, func() (core.HelperTask, error) { return s.HelperTaskByContact(ctx, id) }} {
		row, err := got()
		if err != nil || row != task {
			t.Fatalf("task round trip: %+v %v", row, err)
		}
	}
	task.Checks = 3
	task.Failures = 0
	task.NextDue = 20
	task.LastOutcome = "quiet"
	if err := s.UpdateHelperTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if row, err := s.HelperTask(ctx, "a"); err != nil || row != task {
		t.Fatalf("updated task: %+v %v", row, err)
	}
	if n, err := s.CountActiveHelperTasks(ctx); err != nil || n != 1 {
		t.Fatalf("active: %d %v", n, err)
	}
	if n, err := s.NextHelperDue(ctx); err != nil || n != 20 {
		t.Fatalf("next: %d %v", n, err)
	}
	if rows, err := s.DueHelperTasks(ctx, 19, 10); err != nil || len(rows) != 0 {
		t.Fatalf("early due: %v %v", rows, err)
	}
	if rows, err := s.DueHelperTasks(ctx, 20, 10); err != nil || len(rows) != 1 {
		t.Fatalf("due: %v %v", rows, err)
	}
	if rows, err := s.PendingHelperTasks(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("pending: %v %v", rows, err)
	}
	// The spec's lower status timestamp bound is inclusive.
	if rows, err := s.ListHelperTasks(ctx, core.HelperTaskFilter{ContextID: "ctx", State: state, After: 5, Limit: 1}); err != nil || len(rows) != 1 {
		t.Fatalf("inclusive filter: %v %v", rows, err)
	}
	if n, err := s.CountHelperTasks(ctx, core.HelperTaskFilter{After: 6}); err != nil || n != 0 {
		t.Fatalf("filtered count: %d %v", n, err)
	}
	if rows, err := s.ListHelperTasks(ctx, core.HelperTaskFilter{Cursor: "1:a", Limit: 10}); err != nil || len(rows) != 0 {
		t.Fatalf("cursor: %v %v", rows, err)
	}
	if _, err := s.ListHelperTasks(ctx, core.HelperTaskFilter{Cursor: "bad"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	cfg := core.PushConfig{TaskID: "a", ID: "p", URL: "http://localhost/push", Token: "token", AuthScheme: "Bearer", AuthCredentials: "secret", CreatedAt: 1}
	if err := s.PutPushConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.URL = "http://localhost/new"
	if err := s.PutPushConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PushConfig(ctx, "a", "p"); err != nil || got != cfg {
		t.Fatalf("config: %v %v", got, err)
	}
	if got, err := s.PushConfigs(ctx, "a"); err != nil || !reflect.DeepEqual(got, []core.PushConfig{cfg}) {
		t.Fatalf("configs: %v %v", got, err)
	}
	for range 2 {
		if err := s.DeletePushConfig(ctx, "a", "p"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PushConfig(ctx, "a", "p"); !errors.Is(err, core.ErrTaskNotFound) {
		t.Fatalf("missing config: %v", err)
	}
	if _, err := s.HelperTask(ctx, "missing"); !errors.Is(err, core.ErrTaskNotFound) {
		t.Fatalf("missing task: %v", err)
	}
}

func TestHelperDueAndPending(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	ctx := t.Context()
	for _, row := range []core.HelperTask{
		{ID: "waiting", State: string(a2a.TaskStateInputRequired), UntilTS: 10},
		{ID: "working", State: string(a2a.TaskStateWorking), NextDue: 30, UntilTS: 15},
		{ID: "ended", State: string(a2a.TaskStateCompleted), PushedState: string(a2a.TaskStateCompleted)},
		{ID: "undelivered", State: string(a2a.TaskStateCanceled)},
	} {
		if err := s.InsertHelperTask(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if due, err := s.NextHelperDue(ctx); err != nil || due != 10 {
		t.Fatalf("waiting deadline: %v %v", due, err)
	}
	rows, err := s.DueHelperTasks(ctx, 15, 10)
	if err != nil || len(rows) != 2 || rows[0].ID != "waiting" || rows[1].ID != "working" {
		t.Fatalf("due order and expiry: %+v %v", rows, err)
	}
	if rows, err := s.PendingHelperTasks(ctx); err != nil || len(rows) != 3 {
		t.Fatalf("pending: %v %v", rows, err)
	}
}

func TestRemoteHelperStorage(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	ctx := t.Context()
	row := core.RemoteHelper{Parent: 7, AgentURL: "http://helper", TaskID: "task", ContextID: "ctx", Title: "watch", State: string(a2a.TaskStateWorking), StatusMessage: "working", HelperContact: 9, EveryMS: 10, UntilTS: 50, NextCheck: 20, Checks: 2, Reports: 1, LastCheckAt: 10, LastReportAt: 11, LastOutcome: "reported", PushToken: "token", PushSecret: "secret", CancelRequested: true, NotifiedState: "old", CreatedAt: 1, UpdatedAt: 3}
	handle, err := s.InsertRemoteHelper(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	row.Handle = handle
	getters := []func() (core.RemoteHelper, error){func() (core.RemoteHelper, error) { return s.RemoteHelper(ctx, handle) }, func() (core.RemoteHelper, error) { return s.RemoteHelperByToken(ctx, "token") }, func() (core.RemoteHelper, error) { return s.RemoteHelperByTask(ctx, "task") }}
	for _, get := range getters {
		got, err := get()
		if err != nil || got != row {
			t.Fatalf("round trip: %+v %v", got, err)
		}
	}
	row.Reports = 3
	row.UpdatedAt = 5
	row.CancelRequested = false
	if err := s.UpdateRemoteHelper(ctx, row); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RemoteHelper(ctx, handle); err != nil || got != row {
		t.Fatalf("update: %+v %v", got, err)
	}
	if got, err := s.ListRemoteHelpers(ctx, 7, 20); err != nil || !reflect.DeepEqual(got, []core.RemoteHelper{row}) {
		t.Fatalf("list: %+v %v", got, err)
	}
	if n, err := s.CountActiveRemoteHelpers(ctx, 7); err != nil || n != 1 {
		t.Fatalf("active: %d %v", n, err)
	}
	if rows, err := s.ListRemoteHelpers(ctx, 8, 20); err != nil || len(rows) != 0 {
		t.Fatalf("foreign parent: %v %v", rows, err)
	}
	if yes, err := s.SeenArtifact(ctx, handle, "report-1"); err != nil || yes {
		t.Fatalf("unseen: %v %v", yes, err)
	}
	for range 2 {
		if err := s.MarkArtifactSeen(ctx, handle, "report-1"); err != nil {
			t.Fatal(err)
		}
	}
	if yes, err := s.SeenArtifact(ctx, handle, "report-1"); err != nil || !yes {
		t.Fatalf("seen: %v %v", yes, err)
	}
	for range 2 {
		if err := s.DeleteRemoteHelper(ctx, handle); err != nil {
			t.Fatal(err)
		}
	}
	for _, get := range getters {
		if _, err := get(); !errors.Is(err, core.ErrTaskNotFound) {
			t.Fatalf("missing: %v", err)
		}
	}
}

func TestHelpersMigrationFromV3(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "v3.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_init.sql", "0002_contact_names.sql", "0003_sessions.sql"} {
		ddl, err := schema.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), string(ddl)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO contacts (agent, last_active) VALUES ('{"system":"Ada"}', 1); PRAGMA user_version = 3;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := openAt(t, path, eventlog.Options{})
	cs, err := s.Contacts(t.Context(), 10)
	if err != nil || len(cs) != 1 || cs[0].Agent.Helper {
		t.Fatalf("existing contact changed: %v %v", cs, err)
	}
	if err := s.InsertHelperTask(t.Context(), core.HelperTask{ID: "new"}); err != nil {
		t.Fatal(err)
	}
}
