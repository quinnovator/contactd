package mods

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/quinnovator/contactd/internal/tui/mods/wire"
	"github.com/quinnovator/contactd/internal/tui/safe"
	"github.com/quinnovator/contactd/internal/tui/toml"
)

// ManifestFile is the name of a mod's manifest in its directory.
const ManifestFile = "mod.toml"

// The events a segment can be refreshed on. A manifest may name a duration
// of at least one second instead.
const (
	// RefreshModelCall asks again after each new model-call record.
	RefreshModelCall = "model_call"
	// RefreshTurn asks again when a turn ends.
	RefreshTurn = "turn"
	// RefreshState asks again when the connection or the live state changes.
	RefreshState = "state"
)

// The limits of a manifest.
const (
	maxNameLen  = 32
	maxDecls    = 32 // of each of segments, commands and renderers
	maxKindRest = 64 // the part of a renderer kind after "code:" or "tool:"
	minInterval = time.Second
)

// Manifest is a mod.toml: what a mod is, what it adds and what it may read.
type Manifest struct {
	// ABI is the contract the mod was written to. Only wire.ABI loads.
	ABI int
	// Name is the mod's name, which is also its directory's. Module is the
	// file name of the module in that directory.
	Name, Version, Description, Module string
	// SHA256 pins the module, in lower-case hex. It is empty when the
	// manifest pins nothing.
	SHA256 string
	// Read is what the mod may read, in the order of Grants.
	Read []Grant
	// Budget is the mod's own budget for a call. Zero means the host's; a
	// manifest may only lower it.
	Budget    time.Duration
	Segments  []SegmentDecl
	Commands  []CommandDecl
	Renderers []RendererDecl
}

// SegmentDecl declares one status-line segment.
type SegmentDecl struct {
	Name string
	// Refresh is when the segment is asked for again: RefreshModelCall,
	// RefreshTurn, RefreshState, or a duration such as "30s".
	Refresh string
}

// Every returns the interval of a segment refreshed on a timer, and zero for
// one refreshed on an event.
func (s SegmentDecl) Every() time.Duration {
	switch s.Refresh {
	case RefreshModelCall, RefreshTurn, RefreshState:
		return 0
	}
	d, err := time.ParseDuration(s.Refresh)
	if err != nil {
		return 0
	}
	return d
}

// CommandDecl declares one slash command.
type CommandDecl struct {
	// Name is what follows the slash. Usage names the arguments, and
	// Summary is one line on what the command does.
	Name, Usage, Summary string
}

// RendererDecl declares that the mod draws one kind of item.
type RendererDecl struct {
	// Kind is "code:<language>" or "tool:<tool name>".
	Kind string
}

// Has reports whether the manifest holds a grant.
func (m Manifest) Has(g Grant) bool { return slices.Contains(m.Read, g) }

// ParseManifest reads a mod.toml. dirName is the name of the directory it was
// found in, which the mod's name must equal.
//
// Text a person will read (the version, the description, a command's usage
// and summary) is made safe to draw. A key the host does not know is
// skipped, so an older client reads a newer manifest; a grant it does not
// know is an error, since the list of grants is closed.
func ParseManifest(data []byte, dirName string) (Manifest, error) {
	m, err := parseManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	if m.Name != dirName {
		return Manifest{}, fmt.Errorf("name %q must equal the directory name %q", m.Name, dirName)
	}
	return m, nil
}

// parseManifest reads a manifest without knowing where it was found.
func parseManifest(data []byte) (Manifest, error) {
	doc, err := toml.Parse(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	m, err := readManifest(doc)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	return m, nil
}

func readManifest(doc *toml.Doc) (Manifest, error) {
	root := doc.Root()
	var m Manifest

	abi, ok, err := root.Int("abi")
	if err != nil {
		return m, err //nolint:wrapcheck // the caller names the file
	}
	if !ok {
		return m, errors.New("abi is missing")
	}
	if abi != wire.ABI {
		return m, fmt.Errorf("abi %d is not supported: this client speaks abi %d", abi, wire.ABI)
	}
	m.ABI = wire.ABI

	text := map[string]*string{
		"name": &m.Name, "version": &m.Version, "description": &m.Description,
		"module": &m.Module, "sha256": &m.SHA256,
	}
	for _, key := range []string{"name", "version", "description", "module", "sha256"} {
		v, _, err := root.String(key)
		if err != nil {
			return m, err //nolint:wrapcheck // the caller names the file
		}
		*text[key] = v
	}
	if !validName(m.Name, false) {
		return m, fmt.Errorf("name %q must be a lower-case letter, then at most 31 of a-z, 0-9 and -", m.Name)
	}
	m.Version = safe.Line(m.Version, 32, false)
	m.Description = safe.Line(m.Description, 240, false)
	if err := checkModuleName(m.Module); err != nil {
		return m, err
	}
	if m.SHA256, err = checkPin(m.SHA256); err != nil {
		return m, err
	}
	if m.Read, err = readGrants(root); err != nil {
		return m, err
	}
	if m.Budget, err = readBudget(root); err != nil {
		return m, err
	}
	if m.Segments, err = readSegments(doc); err != nil {
		return m, err
	}
	if m.Commands, err = readCommands(doc); err != nil {
		return m, err
	}
	if m.Renderers, err = readRenderers(doc); err != nil {
		return m, err
	}
	if len(m.Renderers) > 0 && !m.Has(GrantMessages) {
		return m, fmt.Errorf("a renderer is handed message text: add %q to read", GrantMessages)
	}
	return m, nil
}

// validName reports whether s can name a mod, a segment or a command: a
// lower-case letter, then at most 31 of a-z, 0-9 and "-". A segment or a
// command may also use "_".
func validName(s string, underscore bool) bool {
	if s == "" || len(s) > maxNameLen || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		case c == '_' && underscore:
		default:
			return false
		}
	}
	return true
}

// checkModuleName accepts a bare file name and nothing that could leave the
// mod's directory.
func checkModuleName(s string) error {
	switch {
	case s == "":
		return errors.New("module is missing")
	case len(s) > 64:
		return errors.New("module is a file name of at most 64 bytes")
	case strings.EqualFold(s, ManifestFile):
		// In any case: many file systems take MOD.TOML for the same file.
		return errors.New("module cannot be the manifest")
	case s[0] == '.':
		return fmt.Errorf("module %q must be a bare file name that does not start with a dot", s)
	}
	for i := range len(s) {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == '_'
		if !ok {
			return fmt.Errorf("module %q must be a bare file name of letters, digits, '.', '-' and '_'", s)
		}
	}
	return nil
}

// checkPin accepts no pin, or 64 hex digits, and returns them in lower case.
func checkPin(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	s = strings.ToLower(s)
	if len(s) != 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return "", errors.New("sha256 must be 64 hex digits, or empty")
	}
	return s, nil
}

func readGrants(root *toml.Table) ([]Grant, error) {
	names, _, err := root.Strings("read")
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the file
	}
	all := Grants()
	for _, n := range names {
		if !slices.Contains(all, Grant(n)) {
			return nil, lineError(root.KeyLine("read"), fmt.Sprintf("read: unknown grant %q", safe.Line(n, 40, false)))
		}
	}
	var out []Grant
	for _, g := range all {
		if slices.Contains(names, string(g)) {
			out = append(out, g)
		}
	}
	return out, nil
}

func readBudget(root *toml.Table) (time.Duration, error) {
	s, ok, err := root.String("budget")
	if err != nil {
		return 0, err //nolint:wrapcheck // the caller names the file
	}
	if !ok || s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, lineError(root.KeyLine("budget"), `budget must be a duration above zero, such as "2ms"`)
	}
	return d, nil
}

func readSegments(doc *toml.Doc) ([]SegmentDecl, error) {
	tables := doc.Named("segment")
	if len(tables) > maxDecls {
		return nil, fmt.Errorf("more than %d segments", maxDecls)
	}
	out := make([]SegmentDecl, 0, len(tables))
	for _, t := range tables {
		name, err := declName(t, "segment")
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(out, func(s SegmentDecl) bool { return s.Name == name }) {
			return nil, lineError(t.Line, fmt.Sprintf("segment %q is declared twice", name))
		}
		refresh, _, err := t.String("refresh")
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller names the file
		}
		if !validRefresh(refresh) {
			return nil, lineError(max(t.KeyLine("refresh"), t.Line),
				"refresh must be model_call, turn, state or a duration of at least 1s")
		}
		out = append(out, SegmentDecl{Name: name, Refresh: refresh})
	}
	return out, nil
}

func validRefresh(s string) bool {
	switch s {
	case RefreshModelCall, RefreshTurn, RefreshState:
		return true
	}
	d, err := time.ParseDuration(s)
	return err == nil && d >= minInterval
}

func readCommands(doc *toml.Doc) ([]CommandDecl, error) {
	tables := doc.Named("command")
	if len(tables) > maxDecls {
		return nil, fmt.Errorf("more than %d commands", maxDecls)
	}
	out := make([]CommandDecl, 0, len(tables))
	for _, t := range tables {
		name, err := declName(t, "command")
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(out, func(c CommandDecl) bool { return c.Name == name }) {
			return nil, lineError(t.Line, fmt.Sprintf("command %q is declared twice", name))
		}
		usage, _, err := t.String("usage")
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller names the file
		}
		summary, _, err := t.String("summary")
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller names the file
		}
		out = append(out, CommandDecl{
			Name:    name,
			Usage:   safe.Line(usage, 64, false),
			Summary: safe.Line(summary, 120, false),
		})
	}
	return out, nil
}

func readRenderers(doc *toml.Doc) ([]RendererDecl, error) {
	tables := doc.Named("renderer")
	if len(tables) > maxDecls {
		return nil, fmt.Errorf("more than %d renderers", maxDecls)
	}
	out := make([]RendererDecl, 0, len(tables))
	for _, t := range tables {
		kind, _, err := t.String("kind")
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller names the file
		}
		if !validKind(kind) {
			return nil, lineError(max(t.KeyLine("kind"), t.Line),
				"kind must be code:<language> or tool:<tool name>")
		}
		if slices.Contains(out, RendererDecl{Kind: kind}) {
			return nil, lineError(t.Line, fmt.Sprintf("renderer %q is declared twice", kind))
		}
		out = append(out, RendererDecl{Kind: kind})
	}
	return out, nil
}

// declName reads the name of a [[segment]] or a [[command]].
func declName(t *toml.Table, what string) (string, error) {
	name, _, err := t.String("name")
	if err != nil {
		return "", err //nolint:wrapcheck // the caller names the file
	}
	if !validName(name, true) {
		return "", lineError(max(t.KeyLine("name"), t.Line),
			what+" name must be a lower-case letter, then at most 31 of a-z, 0-9, - and _")
	}
	return name, nil
}

// validKind reports whether s is "code:" or "tool:" and then a short name
// with no space or control character in it.
func validKind(s string) bool {
	rest, ok := strings.CutPrefix(s, "code:")
	if !ok {
		rest, ok = strings.CutPrefix(s, "tool:")
	}
	if !ok || rest == "" || len(rest) > maxKindRest {
		return false
	}
	for i := range len(rest) {
		c := rest[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_.+#-", c) >= 0
		if !ok {
			return false
		}
	}
	return true
}

// lineError is a refusal that names the manifest's line, as the parser's own
// errors do.
func lineError(line int, msg string) error {
	return &toml.Error{Line: line, Msg: msg}
}

// checkBudget refuses a manifest that asks for more time than the host
// gives: a manifest may only lower the budget.
func (m Manifest) checkBudget(host time.Duration) error {
	if m.Budget > host {
		return fmt.Errorf("%s: budget %s is above the host's %s: a mod may only lower it", ManifestFile, m.Budget, host)
	}
	return nil
}

// summary lists what a mod adds, for the log.
func (m Manifest) summary() string {
	return fmt.Sprintf("%d segments, %d commands, %d renderers", len(m.Segments), len(m.Commands), len(m.Renderers))
}
