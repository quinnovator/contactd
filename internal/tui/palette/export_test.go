package palette

import "github.com/quinnovator/contactd/internal/tui/commands"

// Matches returns the filtered list itself, not a copy, so a test can tell
// whether it was read from the registry again.
func (m *Model) Matches() []commands.Spec { return m.matches }
