// Package eventlog is the append-only event log on SQLite (Piece 1): one
// file in WAL mode, a single writer goroutine doing adaptive group commit,
// and a separate pool of readers.
package eventlog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/eventlog/sqlcgen"
	"github.com/quinnovator/contactd/sql/schema"
)

// Options tunes the store. The zero value is valid.
type Options struct {
	// SyncFull selects synchronous=FULL: no acknowledged message is lost on
	// power failure, at the price of an fsync per commit. The default,
	// NORMAL, can lose the last few commits on power loss but never corrupts
	// the file. Group commit amortizes the fsync either way.
	SyncFull bool
	// CacheKiB is the per-connection page cache. Default 16 MiB.
	CacheKiB int
	// Readers sizes the read pool. Default GOMAXPROCS.
	Readers int
	// BusyTimeout is how long a connection waits for a lock held by another
	// process (a backup, a shell) before failing. Default 5s.
	BusyTimeout time.Duration
}

// Store is the event log. Reads run on the reader pool and are safe from any
// goroutine. Writes go through Submit and are applied by Run.
type Store struct {
	wdb *sql.DB // exactly one connection
	rdb *sql.DB
	rq  *sqlcgen.Queries

	insertEvent *sql.Stmt
	in          chan core.Write
	stopped     chan struct{} // closed when Run has drained and returned
	// failed holds contacts with a failed write and no Reset since. Only the
	// writer goroutine touches it.
	failed map[core.ContactID]struct{}
}

// Open opens (creating and migrating if needed) the database at path.
func Open(ctx context.Context, path string, opt Options) (*Store, error) {
	if opt.CacheKiB <= 0 {
		opt.CacheKiB = 16 * 1024
	}
	if opt.Readers <= 0 {
		opt.Readers = runtime.GOMAXPROCS(0)
	}
	if opt.BusyTimeout <= 0 {
		opt.BusyTimeout = 5 * time.Second
	}
	busy := strconv.FormatInt(opt.BusyTimeout.Milliseconds(), 10)
	sync := "NORMAL"
	if opt.SyncFull {
		sync = "FULL"
	}
	hook := func(conn *sqlite3.SQLiteConn) error {
		// The pragmas the driver has no DSN parameter for.
		pragmas := "PRAGMA temp_store=MEMORY; PRAGMA mmap_size=268435456; PRAGMA cache_size=-" +
			strconv.Itoa(opt.CacheKiB) + ";"
		if _, err := conn.Exec(pragmas, nil); err != nil {
			return fmt.Errorf("set pragmas: %w", err)
		}
		return nil
	}
	dsn := func(extra string) string {
		// A file: URI lets the driver take parameters; only the characters
		// that would end the path need escaping.
		escaped := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
		return "file:" + escaped + "?_journal_mode=WAL&_busy_timeout=" + busy + "&_synchronous=" + sync + extra
	}

	s := &Store{
		wdb:     sql.OpenDB(connector{drv: &sqlite3.SQLiteDriver{ConnectHook: hook}, dsn: dsn("&_txlock=immediate")}),
		rdb:     sql.OpenDB(connector{drv: &sqlite3.SQLiteDriver{ConnectHook: hook}, dsn: dsn("&_query_only=true")}),
		in:      make(chan core.Write, 4096),
		stopped: make(chan struct{}),
		failed:  make(map[core.ContactID]struct{}),
	}
	s.wdb.SetMaxOpenConns(1)
	s.rdb.SetMaxOpenConns(opt.Readers)
	s.rdb.SetMaxIdleConns(opt.Readers)
	s.rq = sqlcgen.New(s.rdb)

	if err := migrate(ctx, s.wdb); err != nil {
		return nil, errors.Join(err, s.wdb.Close(), s.rdb.Close())
	}
	// Rule 9: the batched INSERT is the one hand-written statement, because
	// it must be a reusable prepared statement inside the batch transaction.
	stmt, err := s.wdb.PrepareContext(ctx,
		`INSERT INTO events (contact, seq, ts, kind, body) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare event insert: %w", err), s.wdb.Close(), s.rdb.Close())
	}
	s.insertEvent = stmt
	return s, nil
}

// Close closes the database. Call it after Run has returned.
func (s *Store) Close() error {
	if err := errors.Join(s.insertEvent.Close(), s.rdb.Close(), s.wdb.Close()); err != nil {
		return fmt.Errorf("close event log: %w", err)
	}
	return nil
}

// CreateContact inserts an unnamed contact and returns its ID.
func (s *Store) CreateContact(ctx context.Context, agent core.AgentConfig, profile string, now int64) (core.ContactID, error) {
	return s.CreateNamed(ctx, "", agent, profile, now)
}

// CreateNamed inserts a contact with a name and returns its ID. An empty
// name is no name. A name another contact has is core.ErrNameTaken.
func (s *Store) CreateNamed(ctx context.Context, name string, agent core.AgentConfig, profile string, now int64) (core.ContactID, error) {
	raw, err := json.Marshal(agent)
	if err != nil {
		return 0, fmt.Errorf("encode agent config: %w", err)
	}
	// Contacts are created far less often than events are appended, so this
	// takes the writer connection directly rather than a slot in the batch.
	// database/sql serializes it with the group-commit transaction.
	id, err := sqlcgen.New(s.wdb).InsertContact(ctx, sqlcgen.InsertContactParams{
		Name:       name,
		Parent:     i64(uint64(agent.Parent)),
		Agent:      string(raw),
		Profile:    []byte(profile),
		LastActive: now,
	})
	if isUnique(err) {
		return 0, fmt.Errorf("name %q: %w", name, core.ErrNameTaken)
	}
	if err != nil {
		return 0, fmt.Errorf("insert contact: %w", err)
	}
	return core.ContactID(id), nil //nolint:gosec // rowids are positive
}

// RenameContact gives a contact a name, or with an empty name takes its name
// away.
func (s *Store) RenameContact(ctx context.Context, id core.ContactID, name string) error {
	n, err := sqlcgen.New(s.wdb).RenameContact(ctx, sqlcgen.RenameContactParams{Name: name, ID: i64(uint64(id))})
	switch {
	case isUnique(err):
		return fmt.Errorf("name %q: %w", name, core.ErrNameTaken)
	case err != nil:
		return fmt.Errorf("rename contact %d: %w", id, err)
	case n == 0:
		return fmt.Errorf("contact %d: %w", id, core.ErrContactNotFound)
	}
	return nil
}

// isUnique reports whether err is a unique-index violation.
func isUnique(err error) bool {
	var se sqlite3.Error
	return errors.As(err, &se) && se.ExtendedCode == sqlite3.ErrConstraintUnique
}

// Contact loads one contact row.
func (s *Store) Contact(ctx context.Context, id core.ContactID) (core.Contact, error) {
	row, err := s.rq.GetContact(ctx, i64(uint64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Contact{}, fmt.Errorf("contact %d: %w", id, core.ErrContactNotFound)
	}
	if err != nil {
		return core.Contact{}, fmt.Errorf("load contact %d: %w", id, err)
	}
	return toContact(row.ID, row.Name, row.Agent, row.Profile, row.LastActive)
}

// ContactByName loads the contact with exactly this name.
func (s *Store) ContactByName(ctx context.Context, name string) (core.Contact, error) {
	if name == "" {
		return core.Contact{}, fmt.Errorf("contact %q: %w", name, core.ErrContactNotFound)
	}
	row, err := s.rq.GetContactByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Contact{}, fmt.Errorf("contact %q: %w", name, core.ErrContactNotFound)
	}
	if err != nil {
		return core.Contact{}, fmt.Errorf("load contact %q: %w", name, err)
	}
	return toContact(row.ID, row.Name, row.Agent, row.Profile, row.LastActive)
}

// Contacts lists the contacts a person talks to, most recently active first.
// Sub-agents are left out: they are reached through their parent's log.
func (s *Store) Contacts(ctx context.Context, limit int) ([]core.Contact, error) {
	rows, err := s.rq.ListContacts(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	out := make([]core.Contact, len(rows))
	for i, r := range rows {
		if out[i], err = toContact(r.ID, r.Name, r.Agent, r.Profile, r.LastActive); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func toContact(id int64, name, agent string, profile []byte, lastActive int64) (core.Contact, error) {
	c := core.Contact{ID: core.ContactID(u64(id)), Name: name, Profile: string(profile), LastActive: lastActive}
	if err := json.Unmarshal([]byte(agent), &c.Agent); err != nil {
		return core.Contact{}, fmt.Errorf("decode agent config of contact %d: %w", id, err)
	}
	return c, nil
}

// CountContacts returns the number of contacts.
func (s *Store) CountContacts(ctx context.Context) (int64, error) {
	n, err := s.rq.CountContacts(ctx)
	if err != nil {
		return 0, fmt.Errorf("count contacts: %w", err)
	}
	return n, nil
}

// Hydrate reads what an actor needs to wake a contact: the contact row, the
// last tail events (one range scan) and the episode rows. Message vectors
// stay on disk.
func (s *Store) Hydrate(ctx context.Context, id core.ContactID, tail int) (core.Hydrated, error) {
	c, err := s.Contact(ctx, id)
	if err != nil {
		return core.Hydrated{}, err
	}
	rows, err := s.rq.TailEvents(ctx, sqlcgen.TailEventsParams{Contact: i64(uint64(id)), Limit: int64(tail)})
	if err != nil {
		return core.Hydrated{}, fmt.Errorf("load tail of contact %d: %w", id, err)
	}
	h := core.Hydrated{Contact: c, Tail: toEvents(rows), NextSeq: 1}
	// TailEvents scans backwards; put it in log order.
	for i, j := 0, len(h.Tail)-1; i < j; i, j = i+1, j-1 {
		h.Tail[i], h.Tail[j] = h.Tail[j], h.Tail[i]
	}
	if n := len(h.Tail); n > 0 {
		h.NextSeq = h.Tail[n-1].Seq + 1
	}
	if h.Episodes, err = s.Episodes(ctx, id); err != nil {
		return core.Hydrated{}, err
	}
	return h, nil
}

// Episodes returns a contact's episode rows in ID order.
func (s *Store) Episodes(ctx context.Context, id core.ContactID) ([]core.EpisodeRow, error) {
	rows, err := s.rq.Episodes(ctx, i64(uint64(id)))
	if err != nil {
		return nil, fmt.Errorf("load episodes of contact %d: %w", id, err)
	}
	out := make([]core.EpisodeRow, len(rows))
	for i, r := range rows {
		out[i] = core.EpisodeRow{
			ID:         uint32(r.Ep), //nolint:gosec // written from a uint32
			Start:      u64(r.StartSeq),
			End:        u64(r.EndSeq),
			Centroid:   r.Centroid,
			SummarySeq: u64(r.SummarySeq.Int64),
		}
	}
	return out, nil
}

// EventsAfter pages forward through a contact's history.
func (s *Store) EventsAfter(ctx context.Context, id core.ContactID, after uint64, limit int) ([]core.Event, error) {
	rows, err := s.rq.EventsAfter(ctx, sqlcgen.EventsAfterParams{
		Contact: i64(uint64(id)), Seq: i64(after), Limit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("load events of contact %d: %w", id, err)
	}
	return toEvents(rows), nil
}

// EventsAfterOfKinds pages forward through the events of the given kinds.
func (s *Store) EventsAfterOfKinds(ctx context.Context, id core.ContactID, after uint64, limit int, kinds core.KindSet) ([]core.Event, error) {
	rows, err := s.rq.EventsAfterOfKinds(ctx, sqlcgen.EventsAfterOfKindsParams{
		Contact: i64(uint64(id)), After: i64(after), Kinds: int64(kinds), Page: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("load events of contact %d: %w", id, err)
	}
	return toEvents(rows), nil
}

// EventsBefore pages backward: it returns, in log order, the newest limit
// events of the given kinds with seq < before. A before of zero starts from
// the end of the log.
func (s *Store) EventsBefore(ctx context.Context, id core.ContactID, before uint64, limit int, kinds core.KindSet) ([]core.Event, error) {
	if before == 0 {
		before = math.MaxInt64
	}
	rows, err := s.rq.EventsBeforeOfKinds(ctx, sqlcgen.EventsBeforeOfKindsParams{
		Contact: i64(uint64(id)), Before: i64(before), Kinds: int64(kinds), Page: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("load events of contact %d: %w", id, err)
	}
	events := toEvents(rows)
	slices.Reverse(events)
	return events, nil
}

// EventsRange returns up to limit events with from <= seq <= to.
func (s *Store) EventsRange(ctx context.Context, id core.ContactID, from, to uint64, limit int) ([]core.Event, error) {
	rows, err := s.rq.EventsRange(ctx, sqlcgen.EventsRangeParams{
		Contact: i64(uint64(id)), Seq: i64(from), Seq_2: i64(to), Limit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("load events of contact %d: %w", id, err)
	}
	return toEvents(rows), nil
}

// Event returns the event at exactly seq.
func (s *Store) Event(ctx context.Context, id core.ContactID, seq uint64) (core.Event, error) {
	row, err := s.rq.GetEvent(ctx, sqlcgen.GetEventParams{Contact: i64(uint64(id)), Seq: i64(seq)})
	if err != nil {
		return core.Event{}, fmt.Errorf("load event %d of contact %d: %w", seq, id, err)
	}
	return toEvent(row), nil
}

// MessagesAfter returns user and agent messages with seq > after. An actor
// uses it on wake-up to find messages that were never embedded.
func (s *Store) MessagesAfter(ctx context.Context, id core.ContactID, after uint64, limit int) ([]core.Event, error) {
	rows, err := s.rq.MessagesAfter(ctx, sqlcgen.MessagesAfterParams{
		Contact: i64(uint64(id)), Seq: i64(after), Limit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("load messages of contact %d: %w", id, err)
	}
	return toEvents(rows), nil
}

// LatestOfKind returns the newest events of one kind, newest first.
func (s *Store) LatestOfKind(ctx context.Context, id core.ContactID, kind core.EventKind, limit int) ([]core.Event, error) {
	rows, err := s.rq.LatestOfKind(ctx, sqlcgen.LatestOfKindParams{
		Contact: i64(uint64(id)), Kind: int64(kind), Limit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("load %s events of contact %d: %w", kind, id, err)
	}
	return toEvents(rows), nil
}

// Vectors returns the stored message vectors with from <= seq <= to.
func (s *Store) Vectors(ctx context.Context, id core.ContactID, from, to uint64) ([]core.Vector, error) {
	rows, err := s.rq.VectorsRange(ctx, sqlcgen.VectorsRangeParams{
		Contact: i64(uint64(id)), Seq: i64(from), Seq_2: i64(to),
	})
	if err != nil {
		return nil, fmt.Errorf("load vectors of contact %d: %w", id, err)
	}
	out := make([]core.Vector, len(rows))
	for i, r := range rows {
		v, err := core.DecodeVector(r.Vec)
		if err != nil {
			return nil, fmt.Errorf("vector %d of contact %d: %w", r.Seq, id, err)
		}
		out[i] = core.Vector{Seq: u64(r.Seq), Vec: v}
	}
	return out, nil
}

// ReplaceEpisodes swaps a contact's derived episode index for a rebuilt one.
// It must only be called while no actor is hydrated for the contact.
func (s *Store) ReplaceEpisodes(ctx context.Context, id core.ContactID, rows []core.EpisodeRow) (err error) {
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	q := sqlcgen.New(tx)
	if err = q.DeleteEpisodes(ctx, i64(uint64(id))); err != nil {
		return fmt.Errorf("delete episodes of contact %d: %w", id, err)
	}
	for i := range rows {
		if err = q.UpsertEpisode(ctx, episodeParams(id, &rows[i])); err != nil {
			return fmt.Errorf("insert episode %d of contact %d: %w", rows[i].ID, id, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func episodeParams(id core.ContactID, r *core.EpisodeRow) sqlcgen.UpsertEpisodeParams {
	return sqlcgen.UpsertEpisodeParams{
		Contact:    i64(uint64(id)),
		Ep:         int64(r.ID),
		StartSeq:   i64(r.Start),
		EndSeq:     i64(r.End),
		Centroid:   r.Centroid,
		SummarySeq: sql.NullInt64{Int64: i64(r.SummarySeq), Valid: r.SummarySeq != 0},
	}
}

func toEvents(rows []sqlcgen.Event) []core.Event {
	out := make([]core.Event, len(rows))
	for i, r := range rows {
		out[i] = toEvent(r)
	}
	return out
}

func toEvent(r sqlcgen.Event) core.Event {
	return core.Event{
		Contact: core.ContactID(u64(r.Contact)),
		Seq:     u64(r.Seq),
		TS:      r.Ts,
		Kind:    core.EventKind(r.Kind), //nolint:gosec // written from a uint8
		Body:    r.Body,
	}
}

// SQLite integers are signed 64-bit. IDs and sequence numbers stay far below
// 2^63, so the conversions are lossless.
func i64(v uint64) int64 { return int64(v) }  //nolint:gosec // see above
func u64(v int64) uint64 { return uint64(v) } //nolint:gosec // see above

// migrate applies every embedded migration newer than PRAGMA user_version.
func migrate(ctx context.Context, db *sql.DB) error {
	names, err := fs.Glob(schema.FS, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(names)

	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for _, name := range names {
		prefix, _, _ := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("migration %q has no numeric prefix: %w", name, err)
		}
		if version <= current {
			continue
		}
		ddl, err := fs.ReadFile(schema.FS, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, db, string(ddl), version); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, ddl string, version int) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	// PRAGMA takes no bind parameters; version comes from a file name we ship.
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(version)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// connector opens connections with a driver instance of our own, so the
// connect hook applies without registering a global driver name.
type connector struct {
	drv *sqlite3.SQLiteDriver
	dsn string
}

func (c connector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return conn, nil
}

func (c connector) Driver() driver.Driver { return c.drv }
