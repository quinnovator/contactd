package modscreen_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/tui/mods"
	"github.com/quinnovator/contactd/internal/tui/modscreen"
	"github.com/quinnovator/contactd/internal/tui/theme"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

// TestGolden pins whole screens. The first is the drawing: five mods, four
// on and one off, at 100 by 30.
func TestGolden(t *testing.T) {
	t.Parallel()
	failed := fiveMods()
	failed[1].Status, failed[1].Reason = mods.StatusFailed, "the module imports env.draw, which no mod may"
	failed[1].Calls = 0
	tests := []struct {
		name          string
		th            *theme.Theme
		infos         []mods.Info
		tmux          bool
		keys          []string
		width, height int
		styled        bool
	}{
		{name: "mods-100x30", th: darkTheme(), infos: fiveMods(), width: 100, height: 30},
		{name: "mods-100x30-styled", th: darkTheme(), infos: fiveMods(), width: 100, height: 30, styled: true},
		{name: "mods-100x30-ascii", th: asciiTheme(), infos: fiveMods(), tmux: true, width: 100, height: 30},
		{name: "failed-100x30", th: darkTheme(), infos: failed, keys: []string{"down"}, width: 100, height: 30},
		{name: "empty-100x30", th: darkTheme(), width: 100, height: 30},
		{name: "list-60x16", th: darkTheme(), infos: fiveMods(), tmux: true, width: 60, height: 16},
		{name: "details-60x16", th: darkTheme(), infos: fiveMods(), keys: []string{"enter"}, width: 60, height: 16},
		{name: "list-30x12", th: darkTheme(), infos: fiveMods(), width: 30, height: 12},
		{name: "list-40x16", th: darkTheme(), infos: fiveMods(), tmux: true, width: 40, height: 16},
		{name: "details-80x30", th: darkTheme(), infos: fiveMods(), width: 80, height: 30},
		{name: "empty-60x16", th: darkTheme(), width: 60, height: 16},
		{name: "empty-30x12", th: darkTheme(), width: 30, height: 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := modscreen.New(tt.infos, dirLabel, tt.tmux)
			m.View(tt.th, tt.width, tt.height) // the screen learns its width
			press(t, m, tt.keys...)
			content, cursor := m.View(tt.th, tt.width, tt.height)
			if cursor != nil {
				t.Errorf("cursor = %+v with no input open", cursor)
			}
			if !tt.styled {
				content = ansi.Strip(content)
			}
			if n := strings.Count(content, "\n") + 1; n != tt.height {
				t.Errorf("%d rows, want %d", n, tt.height)
			}
			tuitest.Golden(t, tt.name+".golden", []byte(content+"\n"))
		})
	}
}
