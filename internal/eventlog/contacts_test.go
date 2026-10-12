package eventlog_test

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog"
	"github.com/quinnovator/contactd/sql/schema"
)

func TestContactNames(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	ctx := t.Context()
	agent := core.AgentConfig{System: "be kind"}

	ada, err := s.CreateNamed(ctx, "ada", agent, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNamed(ctx, "ada", agent, "", 100); !errors.Is(err, core.ErrNameTaken) {
		t.Errorf("a second ada: err = %v, want ErrNameTaken", err)
	}
	// No name is not a name: any number of contacts may have none.
	first, err := s.CreateContact(ctx, agent, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateContact(ctx, agent, "", 300)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := s.ContactByName(ctx, "ada"); err != nil || got.ID != ada || got.Name != "ada" {
		t.Errorf("ContactByName(ada) = %+v, %v", got, err)
	}
	for _, name := range []string{"", "Ada", "bob"} {
		if _, err := s.ContactByName(ctx, name); !errors.Is(err, core.ErrContactNotFound) {
			t.Errorf("ContactByName(%q): err = %v, want ErrContactNotFound", name, err)
		}
	}

	if err := s.RenameContact(ctx, first, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameContact(ctx, second, "bob"); !errors.Is(err, core.ErrNameTaken) {
		t.Errorf("renaming to a taken name: err = %v, want ErrNameTaken", err)
	}
	if err := s.RenameContact(ctx, 9999, "carol"); !errors.Is(err, core.ErrContactNotFound) {
		t.Errorf("renaming a missing contact: err = %v, want ErrContactNotFound", err)
	}
	// Renaming changes the name and nothing the prompt is built from.
	if got, _ := s.Contact(ctx, first); got.Name != "bob" || got.Agent.System != "be kind" || got.LastActive != 200 {
		t.Errorf("after the rename: %+v", got)
	}
	if err := s.RenameContact(ctx, first, ""); err != nil {
		t.Errorf("taking a name away: %v", err)
	}
	if err := s.RenameContact(ctx, second, "bob"); err != nil {
		t.Errorf("a name that was given up could not be taken: %v", err)
	}
}

func TestContactsListsPeopleNotSubAgents(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	ctx := t.Context()
	agent := core.AgentConfig{System: "be kind"}
	old, _ := s.CreateNamed(ctx, "old", agent, "", 100)
	recent, _ := s.CreateNamed(ctx, "recent", agent, "", 300)
	middle, _ := s.CreateNamed(ctx, "middle", agent, "", 200)
	child := agent
	child.Parent = recent
	if _, err := s.CreateContact(ctx, child, "", 400); err != nil {
		t.Fatal(err)
	}

	list, err := s.Contacts(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]core.ContactID, 0, len(list))
	for _, c := range list {
		got = append(got, c.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint([]core.ContactID{recent, middle, old}) {
		t.Errorf("Contacts = %v; want the three people, most recently active first, and no sub-agent", got)
	}
	if list[0].Name != "recent" || list[0].Agent.System != "be kind" {
		t.Errorf("first row = %+v", list[0])
	}
	if short, _ := s.Contacts(ctx, 2); len(short) != 2 {
		t.Errorf("limit 2 returned %d", len(short))
	}
}

func TestEventsBeforeAndKinds(t *testing.T) {
	t.Parallel()
	s := open(t, eventlog.Options{})
	ctx := t.Context()
	id := contact(t, s)
	kinds := []core.EventKind{
		core.KindUserMsg, core.KindAttempt, core.KindAgentMsg, core.KindUserMsg,
		core.KindToolCall, core.KindToolResult, core.KindAttempt, core.KindAgentMsg,
	}
	for i, k := range kinds {
		seq := uint64(i + 1)
		if err := s.Append(ctx, core.Write{Contact: id, Event: event(id, seq, k, `{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	messages := core.KindSet(0).With(core.KindUserMsg).With(core.KindAgentMsg)

	for _, tc := range []struct {
		name   string
		before uint64
		limit  int
		kinds  core.KindSet
		want   string
	}{
		{"the end of the log", 0, 3, core.AllKinds, "6 7 8"},
		{"the page before that", 6, 3, core.AllKinds, "3 4 5"},
		{"the first page, short", 3, 3, core.AllKinds, "1 2"},
		{"nothing before the first event", 1, 3, core.AllKinds, ""},
		{"only messages, from the end", 0, 3, messages, "3 4 8"},
		{"only messages, further back", 3, 3, messages, "1"},
	} {
		got, err := s.EventsBefore(ctx, id, tc.before, tc.limit, tc.kinds)
		if err != nil || seqsString(got) != tc.want {
			t.Errorf("%s: EventsBefore(%d, limit %d) = %q, %v; want %q", tc.name, tc.before, tc.limit, seqsString(got), err, tc.want)
		}
	}
	if got, _ := s.EventsAfterOfKinds(ctx, id, 1, 3, messages); seqsString(got) != "3 4 8" {
		t.Errorf("EventsAfterOfKinds(1, messages) = %s", seqsString(got))
	}
	if got, _ := s.EventsAfterOfKinds(ctx, id, 2, 2, core.AllKinds); seqsString(got) != "3 4" {
		t.Errorf("EventsAfterOfKinds(2, all) = %s", seqsString(got))
	}
}

// A database made before contacts had names gains the columns on open, and
// the sub-agents already in it are recognised from their agent configs.
func TestMigrationFromTheFirstSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "contacts.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := schema.FS.ReadFile("0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		string(ddl),
		`INSERT INTO contacts (id, agent, profile, last_active) VALUES (1, '{"system":"parent"}', NULL, 100)`,
		`INSERT INTO contacts (id, agent, profile, last_active) VALUES (2, '{"system":"parent","parent":1}', NULL, 200)`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s := openAt(t, path, eventlog.Options{})
	list, err := s.Contacts(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != 1 || list[0].Name != "" {
		t.Errorf("Contacts after migrating = %+v; want the parent only", list)
	}
	if got, err := s.Contact(t.Context(), 2); err != nil || got.Agent.Parent != 1 {
		t.Errorf("the sub-agent did not survive: %+v, %v", got, err)
	}
	if err := s.RenameContact(t.Context(), 1, "ada"); err != nil {
		t.Errorf("naming a contact from before names: %v", err)
	}
}
