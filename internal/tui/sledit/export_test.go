package sledit

import (
	"slices"

	"github.com/quinnovator/contactd/internal/tui/statusline"
)

// Config returns a copy of the config as it is being edited.
func (m *Model) Config() statusline.Config {
	return statusline.Config{Segments: slices.Clone(m.cfg.Segments)}
}

// Selected returns the ID of the selected segment, or "" when there is none.
func (m *Model) Selected() string {
	if seg := m.selected(); seg != nil {
		return seg.ID()
	}
	return ""
}

// Editing says a field is being edited.
func (m *Model) Editing() bool { return m.ed != nil }
