package sledit_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quinnovator/contactd/internal/tui/sledit"
	"github.com/quinnovator/contactd/internal/tui/statusline"
)

// Whatever a config, a value or a person's typing holds, the screen has as
// many rows as the terminal, none too wide, and nothing in them a terminal
// would act on but styles.
func FuzzView(f *testing.F) {
	f.Add("cache $pct%", "load", "82", "uptime", uint8(100), uint8(30), uint8(0))
	f.Add("\x1b[2J$out\x1b]0;x\x07", "\x1b[31mred", "\x9b31m", "a\tb\r\n", uint8(80), uint8(24), uint8(1))
	f.Add("👨\u200d👩\u200d👧 $1", "名前", "这是一个很长的中文句子用来测试", "👍🏽", uint8(79), uint8(12), uint8(2))
	f.Add("\u202e$out\u2066", "e\u0301\u0301\u0301", "\U0010EEEE\u0301", "\xff\xfe", uint8(40), uint8(8), uint8(3))
	f.Add("$$ ${out} $9 $", "", "", "", uint8(1), uint8(1), uint8(4))
	f.Add("", "x y", " ", "\x00", uint8(0), uint8(0), uint8(5))
	f.Fuzz(func(t *testing.T, format, name, value, typing string, width, height, state uint8) {
		c, _ := statusline.Preset(statusline.DefaultPreset)
		c.Segments = append(c.Segments,
			statusline.Segment{
				Name: name, From: statusline.FromShell, On: true, Side: statusline.Left,
				Format: format, Short: format, Command: typing, Match: value,
			},
			statusline.Segment{Name: name, From: "mod:" + name, On: true, Side: statusline.Right, Format: format},
		)
		c.Segments[0].Format = format
		vals := map[string]statusline.Value{}
		for _, s := range c.Segments {
			vals[s.ID()] = statusline.Value{Vars: map[string]string{"out": value, "1": value, "text": value, "dot": value}, Tone: value}
		}
		term := terminals()[int(state)%len(terminals())]
		th := themeOf(term.caps)
		m := sledit.New(c, value, vals)
		for range int(state) % len(c.Segments) {
			send(t, m, down())
		}
		if width == 0 || height == 0 {
			if content, cursor := m.View(th, int(width), int(height)); content != "" || cursor != nil {
				t.Errorf("a screen of %dx%d is %q", width, height, content)
			}
			return
		}
		checkScreen(t, m, th, int(width), int(height))
		if state&0x40 != 0 {
			m.SaveFailed(typing)
			checkScreen(t, m, th, int(width), int(height))
		}
		if state&0x80 != 0 {
			send(t, m, char('e'), tea.PasteMsg{Content: typing})
			checkScreen(t, m, th, int(width), int(height))
			send(t, m, enter())
			checkScreen(t, m, th, int(width), int(height))
		}
	})
}
