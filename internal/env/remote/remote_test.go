package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/quinnovator/contactd/internal/core"
	"github.com/quinnovator/contactd/internal/obs"
	"github.com/quinnovator/contactd/internal/turn"
)

// wireExec is every field the service's API defines for POST /v1/exec. The
// sandbox decodes strictly into it, so a client that sends anything else gets
// a 400.
type wireExec struct {
	Env       string   `json:"env"`
	Snapshot  string   `json:"snapshot"`
	Session   string   `json:"session"`
	Cmd       []string `json:"cmd"`
	Stdin     string   `json:"stdin"`
	TimeoutMS int64    `json:"timeout_ms"`
	Keep      bool     `json:"keep"`
	TTL       string   `json:"ttl"`
}

type wireResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Exit       int    `json:"exit"`
	SnapshotID string `json:"snapshot_id,omitempty"`
}

// sandbox emulates the snapshot-sandbox service. A snapshot is the list of
// everything run to produce it, which is what an exec prints back, so a test
// can read the chain off stdout. The stdin of a request picks its behaviour;
// see exec.
type sandbox struct {
	*httptest.Server
	token string

	mu        sync.Mutex
	envs      []string
	snapshots map[string][]string
	pinned    []string
	execs     []wireExec
	requests  []string // "METHOD /escaped/path" of every request
	auth      []string // Authorization header of every request
	gone      chan struct{}
}

func newSandbox(t *testing.T, token string) *sandbox {
	t.Helper()
	s := &sandbox{
		token:     token,
		envs:      []string{"python-3.12", "node-22", "re-ghidra"},
		snapshots: map[string][]string{},
		gone:      make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/exec", s.exec)
	mux.HandleFunc("GET /v1/envs/{name}", s.env)
	mux.HandleFunc("POST /v1/snapshots/{id}/pin", s.pin)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.EscapedPath())
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sandbox) env(w http.ResponseWriter, r *http.Request) {
	if !slices.Contains(s.envs, r.PathValue("name")) {
		http.Error(w, "no such environment", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"name": r.PathValue("name"), "template": "tpl-1", "pool": 2})
}

func (s *sandbox) pin(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.snapshots[id]; !ok {
		http.Error(w, "no such snapshot", http.StatusNotFound)
		return
	}
	s.pinned = append(s.pinned, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *sandbox) exec(w http.ResponseWriter, r *http.Request) {
	var req wireExec
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.execs = append(s.execs, req)
	history, known := s.snapshots[req.Snapshot]
	s.mu.Unlock()

	switch {
	case (req.Env != "") == (req.Snapshot != "") || req.Session != "":
		http.Error(w, "want exactly one of env and snapshot", http.StatusBadRequest)
		return
	case len(req.Cmd) == 0 || req.TimeoutMS <= 0:
		http.Error(w, "cmd and timeout_ms are required", http.StatusBadRequest)
		return
	case req.Env != "" && !slices.Contains(s.envs, req.Env):
		http.Error(w, "no such environment", http.StatusNotFound)
		return
	case req.Snapshot != "" && !known:
		http.Error(w, "no such snapshot", http.StatusNotFound)
		return
	}

	ran := strings.Join(req.Cmd, " ")
	if req.Stdin != "" {
		ran += " <<< " + req.Stdin
	}
	history = append(slices.Clone(history), ran)
	res := wireResult{Stdout: strings.Join(history, "\n")}
	keep := req.Keep

	switch verb, arg, _ := strings.Cut(req.Stdin, " "); verb {
	case "exit":
		res.Exit, _ = strconv.Atoi(arg)
		res.Stderr = "Traceback: boom\n"
	case "http500":
		http.Error(w, "sandbox exploded", http.StatusInternalServerError)
		return
	case "garbage":
		_, _ = w.Write([]byte(`{"stdout": "cut off`))
		return
	case "nosnapshot":
		keep = false
	case "hang":
		<-r.Context().Done()
		s.gone <- struct{}{}
		return
	}
	if strings.Contains(ran, "no-such-package") {
		res = wireResult{Exit: 1, Stderr: "ERROR: No matching distribution found for no-such-package\n"}
	}
	if keep {
		s.mu.Lock()
		res.SnapshotID = "snap-" + strconv.Itoa(len(s.snapshots)+1)
		s.snapshots[res.SnapshotID] = history
		s.mu.Unlock()
	}
	_ = json.NewEncoder(w).Encode(res)
}

func (s *sandbox) log() (execs []wireExec, requests, pinned []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.execs), slices.Clone(s.requests), slices.Clone(s.pinned)
}

func (s *sandbox) provider(t *testing.T, tune func(*Config)) *Provider {
	t.Helper()
	cfg := Config{BaseURL: s.URL, Token: s.token, HTTPClient: s.Client(), Logger: obs.Discard()}
	if tune != nil {
		tune(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func python(packages ...string) core.EnvSpec {
	return core.EnvSpec{Tier: 1, Base: "python-3.12", Packages: packages}
}

func open(t *testing.T, p *Provider, spec core.EnvSpec, resume core.SnapshotID) turn.Lease {
	t.Helper()
	l, err := p.Lease(t.Context(), 7, spec, resume)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	t.Cleanup(l.Release)
	return l
}

func run(t *testing.T, l turn.Lease, code string) turn.ExecResult {
	t.Helper()
	res, err := l.Exec(t.Context(), code)
	if err != nil {
		t.Fatalf("Exec(%q): %v", code, err)
	}
	return res
}

func TestFreshLeaseChainsSnapshots(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	l := open(t, s.provider(t, nil), python(), "")

	res := run(t, l, "x = 1")
	if res.Stdout != "python3 - <<< x = 1" || res.ExitCode != 0 || res.Note != "" {
		t.Errorf("first Exec = %+v", res)
	}
	res = run(t, l, "print(x)")
	if res.Stdout != "python3 - <<< x = 1\npython3 - <<< print(x)" || res.Note != "" {
		t.Errorf("second Exec did not run on top of the first: %+v", res)
	}
	run(t, l, "third")

	execs, requests, _ := s.log()
	// The environment is checked before anything runs in it.
	if want := []string{"GET /v1/envs/python-3.12", "POST /v1/exec", "POST /v1/exec", "POST /v1/exec"}; !slices.Equal(requests, want) {
		t.Errorf("requests = %v, want %v", requests, want)
	}
	for i, want := range []wireExec{
		{Env: "python-3.12"},
		{Snapshot: "snap-1"},
		{Snapshot: "snap-2"},
	} {
		got := execs[i]
		if got.Env != want.Env || got.Snapshot != want.Snapshot {
			t.Errorf("exec %d ran from env %q snapshot %q, want env %q snapshot %q", i, got.Env, got.Snapshot, want.Env, want.Snapshot)
		}
		if !got.Keep || !slices.Equal(got.Cmd, []string{"python3", "-"}) || got.TimeoutMS != 120_000 {
			t.Errorf("exec %d = %+v, want keep, python3 - and the default 120s timeout", i, got)
		}
	}
}

func TestResume(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	p := s.provider(t, nil)
	first := open(t, p, python(), "")
	run(t, first, "setup")
	id, err := first.Snapshot(t.Context())
	if err != nil || id != "snap-1" {
		t.Fatalf("Snapshot = %q, %v", id, err)
	}
	first.Release()
	_, before, _ := s.log()

	l := open(t, p, python(), id)
	if _, after, _ := s.log(); len(after) != len(before) {
		t.Errorf("resuming contacted the service: %v", after[len(before):])
	}
	res := run(t, l, "more")
	if res.Stdout != "python3 - <<< setup\npython3 - <<< more" {
		t.Errorf("resumed Exec did not run on the snapshot: %q", res.Stdout)
	}
	if !strings.Contains(res.Note, "restored from a snapshot") || !strings.Contains(res.Note, "files and installed packages are intact") {
		t.Errorf("first Exec of a resumed lease: Note = %q", res.Note)
	}
	if res = run(t, l, "again"); res.Note != "" {
		t.Errorf("second Exec: Note = %q, want empty", res.Note)
	}
}

func TestSnapshotPinsTheHead(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	p := s.provider(t, nil)
	l := open(t, p, python(), "")

	// Nothing has been kept yet, so there is nothing to pin, and "" resumes
	// to the same fresh state.
	if id, err := l.Snapshot(t.Context()); err != nil || id != "" {
		t.Errorf("Snapshot before any Exec = %q, %v; want \"\", nil", id, err)
	}
	run(t, l, "one")
	run(t, l, "two")
	id, err := l.Snapshot(t.Context())
	if err != nil || id != "snap-2" {
		t.Fatalf("Snapshot = %q, %v; want snap-2", id, err)
	}
	// A resumed lease that never ran anything still has its head to pin.
	resumed := open(t, p, python(), "snap-1")
	if id, err := resumed.Snapshot(t.Context()); err != nil || id != "snap-1" {
		t.Errorf("Snapshot of an untouched resumed lease = %q, %v", id, err)
	}
	if _, _, pinned := s.log(); !slices.Equal(pinned, []string{"snap-2", "snap-1"}) {
		t.Errorf("pinned = %v, want [snap-2 snap-1]", pinned)
	}

	gone := open(t, p, python(), "snap-expired")
	if _, err := gone.Snapshot(t.Context()); !errors.Is(err, ErrStatus) {
		t.Errorf("Snapshot of a snapshot the service lost = %v, want ErrStatus", err)
	}
}

func TestNonZeroExitIsAResult(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	l := open(t, s.provider(t, nil), python(), "")
	res := run(t, l, "exit 3")
	if res.ExitCode != 3 || res.Stderr != "Traceback: boom\n" {
		t.Errorf("got %+v, want exit 3 with the traceback", res)
	}
	// A failed run is still a run: the chain moved on.
	run(t, l, "next")
	if execs, _, _ := s.log(); execs[1].Snapshot != "snap-1" {
		t.Errorf("exec after a failure ran from %q, want snap-1", execs[1].Snapshot)
	}
}

func TestServiceFailuresAreErrorsAndLeaveTheChainAlone(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]error{
		"http500":    ErrStatus,
		"garbage":    ErrBadResponse,
		"nosnapshot": ErrBadResponse,
	} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			s := newSandbox(t, "")
			l := open(t, s.provider(t, nil), python(), "")
			run(t, l, "good")
			res, err := l.Exec(t.Context(), code)
			if !errors.Is(err, want) {
				t.Fatalf("Exec = %+v, %v; want %v", res, err, want)
			}
			if code == "http500" && !strings.Contains(err.Error(), "500") {
				t.Errorf("error does not carry the status: %v", err)
			}
			run(t, l, "after")
			if execs, _, _ := s.log(); execs[2].Snapshot != "snap-1" {
				t.Errorf("exec after an error ran from %q, want the last good head snap-1", execs[2].Snapshot)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	l := open(t, s.provider(t, nil), python(), "")
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, err := l.Exec(ctx, "hang"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec = %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("a cancelled Exec took %s to return", took)
	}
	select {
	case <-s.gone:
	case <-time.After(3 * time.Second):
		t.Error("the service never saw the request go away")
	case <-t.Context().Done():
	}

	if _, err := l.Exec(ctx, "late"); !errors.Is(err, context.Canceled) {
		t.Errorf("Exec with a dead ctx = %v, want context.Canceled", err)
	}
}

func TestTimeoutIsBoundedByTheDeadline(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	l := open(t, s.provider(t, func(c *Config) { c.ExecTimeout = 40 * time.Second }), python(), "")
	run(t, l, "unbounded")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := l.Exec(ctx, "bounded"); err != nil {
		t.Fatal(err)
	}
	execs, _, _ := s.log()
	if got := execs[0].TimeoutMS; got != 40_000 {
		t.Errorf("timeout_ms = %d, want the configured 40000", got)
	}
	if got := execs[1].TimeoutMS; got <= 0 || got > 5000 {
		t.Errorf("timeout_ms = %d with 5s left on the context", got)
	}
}

func TestPackages(t *testing.T) {
	t.Parallel()

	t.Run("python installs with pip into the first snapshot", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "")
		l := open(t, s.provider(t, nil), python("numpy", "pandas==2.2"), "")
		res := run(t, l, "import numpy")
		if res.Stdout != "pip install numpy pandas==2.2\npython3 - <<< import numpy" {
			t.Errorf("code did not run on top of the install: %q", res.Stdout)
		}
		execs, _, _ := s.log()
		if boot := execs[0]; boot.Env != "python-3.12" || !boot.Keep || boot.Stdin != "" {
			t.Errorf("bootstrap exec = %+v", boot)
		}
	})

	t.Run("node installs with npm", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "")
		l := open(t, s.provider(t, nil), core.EnvSpec{Tier: 1, Base: "node-22", Packages: []string{"left-pad"}}, "")
		if res := run(t, l, "x"); res.Stdout != "npm install -g left-pad\nnode - <<< x" {
			t.Errorf("stdout = %q", res.Stdout)
		}
	})

	t.Run("a resumed lease installs nothing", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "")
		p := s.provider(t, nil)
		run(t, open(t, p, python(), ""), "seed")
		l := open(t, p, python("numpy"), "snap-1")
		if res := run(t, l, "x"); strings.Contains(res.Stdout, "pip") {
			t.Errorf("packages were installed again on resume: %q", res.Stdout)
		}
	})

	t.Run("refusals happen before anything runs", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "")
		p := s.provider(t, nil)
		for name, spec := range map[string]core.EnvSpec{
			"no installer for the base": {Tier: 1, Base: "re-ghidra", Packages: []string{"capstone"}},
			"option smuggled as a name": python("--index-url=http://evil.example"),
			"empty name":                python(""),
		} {
			if _, err := p.Lease(t.Context(), 7, spec, ""); !errors.Is(err, ErrUnsupportedSpec) {
				t.Errorf("%s: Lease = %v, want ErrUnsupportedSpec", name, err)
			}
		}
		if _, requests, _ := s.log(); len(requests) != 0 {
			t.Errorf("a refused lease still called the service: %v", requests)
		}
	})

	t.Run("a failed install fails the lease", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "")
		_, err := s.provider(t, nil).Lease(t.Context(), 7, python("no-such-package"), "")
		if err == nil || !strings.Contains(err.Error(), "pip exited with status 1") || !strings.Contains(err.Error(), "No matching distribution") {
			t.Errorf("Lease = %v, want pip's failure", err)
		}
	})
}

func TestUnknownEnv(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	p := s.provider(t, nil)
	l, err := p.Lease(t.Context(), 7, core.EnvSpec{Tier: 1, Base: "cobol-85"}, "")
	if !errors.Is(err, ErrUnknownEnv) || l != nil {
		t.Errorf("Lease = %v, %v; want nil, ErrUnknownEnv", l, err)
	}
	if _, err := p.Lease(t.Context(), 7, core.EnvSpec{Tier: 0, Base: "python-3.12"}, ""); !errors.Is(err, ErrUnsupportedSpec) {
		t.Errorf("Lease(tier 0) = %v, want ErrUnsupportedSpec", err)
	}
	// A name cannot reach a different endpoint.
	for _, base := range []string{"..", "../snapshots/snap-1/pin", "a/b", ""} {
		if _, err := p.Lease(t.Context(), 7, core.EnvSpec{Tier: 1, Base: base}, ""); err == nil {
			t.Errorf("Lease(base %q) succeeded", base)
		}
	}
	_, requests, _ := s.log()
	for _, r := range requests {
		if !strings.HasPrefix(r, "GET /v1/envs/") || strings.Count(r, "/") != 3 {
			t.Errorf("a base name escaped its path element: %q", r)
		}
	}
}

func TestRunners(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	p := s.provider(t, func(c *Config) {
		c.Runners = map[string][]string{
			"re-":     {"ghidra-run", "--stdin"},
			"node-22": {"node", "--experimental-vm-modules", "-"},
		}
	})
	for base, want := range map[string]string{
		"python-3.12": "python3 -",                        // default
		"node-22":     "node --experimental-vm-modules -", // the longer prefix beats the default "node"
		"node-20":     "node -",
		"re-ghidra":   "ghidra-run --stdin",
		"alpine":      "sh -s", // no prefix matches
	} {
		if got := strings.Join(p.runner(base), " "); got != want {
			t.Errorf("runner(%q) = %q, want %q", base, got, want)
		}
	}
	l := open(t, p, core.EnvSpec{Tier: 1, Base: "re-ghidra"}, "")
	if res := run(t, l, "analyze"); res.Stdout != "ghidra-run --stdin <<< analyze" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

func TestBearerToken(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", "s3cret"} {
		t.Run("token="+token, func(t *testing.T) {
			t.Parallel()
			s := newSandbox(t, token)
			l := open(t, s.provider(t, nil), python(), "")
			run(t, l, "x")
			if _, err := l.Snapshot(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := ""
			if token != "" {
				want = "Bearer " + token
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.auth) != 3 {
				t.Fatalf("saw %d requests, want 3", len(s.auth))
			}
			for i, got := range s.auth {
				if got != want {
					t.Errorf("request %d: Authorization = %q, want %q", i, got, want)
				}
			}
		})
	}

	t.Run("a rejected token is an error", func(t *testing.T) {
		t.Parallel()
		s := newSandbox(t, "right")
		p := s.provider(t, func(c *Config) { c.Token = "wrong" })
		if _, err := p.Lease(t.Context(), 7, python(), ""); !errors.Is(err, ErrStatus) || !strings.Contains(err.Error(), "401") {
			t.Errorf("Lease = %v, want ErrStatus with 401", err)
		}
	})
}

func TestRelease(t *testing.T) {
	t.Parallel()
	s := newSandbox(t, "")
	l := open(t, s.provider(t, nil), python(), "")
	run(t, l, "x")
	_, before, _ := s.log()
	l.Release()
	l.Release()
	if _, err := l.Exec(t.Context(), "y"); !errors.Is(err, core.ErrClosed) {
		t.Errorf("Exec after Release = %v, want core.ErrClosed", err)
	}
	if _, err := l.Snapshot(t.Context()); !errors.Is(err, core.ErrClosed) {
		t.Errorf("Snapshot after Release = %v, want core.ErrClosed", err)
	}
	if _, after, _ := s.log(); len(after) != len(before) {
		t.Errorf("Release or a released lease called the service: %v", after[len(before):])
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	for _, bad := range []Config{
		{},
		{BaseURL: "sandbox.internal:8080"},
		{BaseURL: "ftp://sandbox.internal"},
		{BaseURL: "http://"},
		{BaseURL: "http://sandbox.internal", Runners: map[string][]string{"ruby": {}}},
		{BaseURL: "http://sandbox.internal", Runners: map[string][]string{"": {"sh"}}},
	} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%+v) succeeded", bad)
		}
	}
	// A service mounted under a path prefix keeps the prefix.
	p, err := New(Config{BaseURL: "https://host.example/sandbox/"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.base.JoinPath("v1", "exec").String(); got != "https://host.example/sandbox/v1/exec" {
		t.Errorf("exec URL = %q", got)
	}
}

func TestLeaseIsObserved(t *testing.T) {
	t.Parallel()
	count := func() uint64 {
		h, ok := obs.EnvLeaseMS.WithLabelValues("1").(prometheus.Histogram)
		if !ok {
			t.Fatal("env_lease_ms is not a histogram")
		}
		var m dto.Metric
		if err := h.Write(&m); err != nil {
			t.Fatal(err)
		}
		return m.GetHistogram().GetSampleCount()
	}
	s := newSandbox(t, "")
	before := count()
	open(t, s.provider(t, nil), python(), "")
	if count() == before {
		t.Error("env_lease_ms{tier=1} did not move")
	}
}
