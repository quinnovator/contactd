package commands

import (
	"slices"
	"strings"
)

// Registry is every command that can be run right now: the core ones, fixed
// when it is made, and the ones mods add and take away. Names are unique and
// in lower case. It is not safe for concurrent use.
type Registry struct {
	core []Spec
	// byMod holds each mod's commands under the names they were given here.
	byMod map[string][]Spec
	// mods is every mod's commands, sorted by name.
	mods []Spec
	// index finds a command by name.
	index map[string]Spec
	rev   uint64
}

// NewRegistry returns a registry holding the core commands, in the order
// given. Their Source is set to "core". A name that ValidName refuses, or
// that an earlier one has, is left out.
func NewRegistry(core []Spec) *Registry {
	r := &Registry{
		core:  make([]Spec, 0, len(core)),
		byMod: make(map[string][]Spec),
		index: make(map[string]Spec, len(core)),
	}
	for _, s := range core {
		if !ValidName(s.Name) {
			continue
		}
		s.Name = strings.ToLower(s.Name)
		s.Source = SourceCore
		if _, taken := r.index[s.Name]; taken {
			continue
		}
		r.core = append(r.core, s)
		r.index[s.Name] = s
	}
	return r
}

// SetMod replaces one mod's commands. Each is registered in lower case with
// Source "mod <mod>". A name that core or another mod has is registered as
// "<mod>.<name>", with the mod's name in lower case; renamed lists those, as
// registered. A command renamed this way keeps that name until the mod is
// set again.
//
// A command that could not be run is left out: one whose name ValidName
// refuses, or that the mod has already used. So is a clash from a mod whose
// own name ValidName refuses, or whose name another mod has in another case:
// "<mod>.<name>" could not be typed, or is not free.
func (r *Registry) SetMod(mod string, specs []Spec) (renamed []string) {
	old := r.byMod[mod]
	for _, s := range old {
		delete(r.index, s.Name)
	}
	kept := make([]Spec, 0, len(specs))
	own := make(map[string]bool, len(specs))
	for _, s := range specs {
		if !ValidName(s.Name) {
			continue
		}
		s.Name = strings.ToLower(s.Name)
		s.Source = "mod " + mod
		if own[s.Name] {
			continue
		}
		own[s.Name] = true
		if _, taken := r.index[s.Name]; taken {
			// Only a renamed command has a dot, and what comes before the
			// dot is its mod in lower case. This mod's own commands were
			// just taken out, so the new name is free unless another mod
			// has this one's name in another case.
			s.Name = strings.ToLower(mod) + "." + s.Name
			if _, taken := r.index[s.Name]; taken || !ValidName(mod) {
				continue
			}
			renamed = append(renamed, s.Name)
		}
		r.index[s.Name] = s
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		delete(r.byMod, mod)
	} else {
		r.byMod[mod] = kept
	}
	if !slices.Equal(old, kept) {
		r.changed()
	}
	return renamed
}

// RemoveMod takes one mod's commands away. A mod that has none is ignored.
func (r *Registry) RemoveMod(mod string) {
	old, ok := r.byMod[mod]
	if !ok {
		return
	}
	for _, s := range old {
		delete(r.index, s.Name)
	}
	delete(r.byMod, mod)
	r.changed()
}

// changed rebuilds the sorted list of mod commands.
func (r *Registry) changed() {
	r.rev++
	r.mods = r.mods[:0]
	for _, specs := range r.byMod {
		r.mods = append(r.mods, specs...)
	}
	slices.SortFunc(r.mods, func(a, b Spec) int { return strings.Compare(a.Name, b.Name) })
}

// Match returns the commands a typed word could mean. typed is the word
// after "/", in any case. Names that start with it come first, then names
// that contain it. Within each group the core commands come in their
// declared order, then the mod commands by name. An empty word matches every
// command. The slice is the caller's own.
func (r *Registry) Match(typed string) []Spec {
	typed = strings.ToLower(typed)
	out := make([]Spec, 0, r.Len())
	for _, group := range [2][]Spec{r.core, r.mods} {
		for _, s := range group {
			if strings.HasPrefix(s.Name, typed) {
				out = append(out, s)
			}
		}
	}
	for _, group := range [2][]Spec{r.core, r.mods} {
		for _, s := range group {
			if !strings.HasPrefix(s.Name, typed) && strings.Contains(s.Name, typed) {
				out = append(out, s)
			}
		}
	}
	return out
}

// Lookup returns the command with exactly this name, in any case.
func (r *Registry) Lookup(name string) (Spec, bool) {
	s, ok := r.index[strings.ToLower(name)]
	return s, ok
}

// Len returns how many commands there are.
func (r *Registry) Len() int { return len(r.core) + len(r.mods) }

// Rev changes whenever the commands do, so a view can tell when a list it
// filtered is stale.
func (r *Registry) Rev() uint64 { return r.rev }

// ValidName reports whether a command can be registered under a name: one or
// more of the ASCII letters, the ASCII digits, "-" and "_". Capitals pass,
// and are registered in lower case.
//
// Such a name can be typed after the slash on any keyboard, cannot be taken
// for a renamed command, which has a dot, and cannot be drawn to look like
// another name or like nothing at all.
func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
