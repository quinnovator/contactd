package inputbox_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/inputbox"
)

// BenchmarkKeyPress is what one typed character costs the box: the key
// through Update, then the lines and the cursor the screen asks for.
func BenchmarkKeyPress(b *testing.B) {
	cases := []struct {
		name  string
		draft string
	}{
		{"one line", "and what would the share have been with the fix?"},
		{"twenty lines", strings.Repeat("and what would the share have been with the fix?\n", 19)},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			m := inputbox.New(unicodeTheme(), placehold)
			m.SetSize(100, 8, true)
			m.SetValue(bc.draft)
			key := tea.KeyPressMsg{Code: 'a', Text: "a"}
			back := tea.KeyPressMsg{Code: tea.KeyBackspace}
			b.ReportAllocs()
			for b.Loop() {
				m.Update(key)
				m.Lines()
				m.Cursor()
				m.Update(back)
			}
		})
	}
}
