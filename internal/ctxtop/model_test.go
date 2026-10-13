package ctxtop

import (
	"fmt"
	"math"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/quinnovator/contactd/internal/diagnostic"
	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestMain(m *testing.M) { tuitest.Main(m) }

func sample() diagnostic.Snapshot {
	s := diagnostic.Snapshot{
		Run: "server-1", Call: 1, Contact: 1, Name: "ada", Route: "deepseek/deepseek-flash", Turn: 8, Step: 2,
		Purpose: "reply", Window: 128000, PredictedCached: 11900, ReportedInput: 18000, ReportedCached: 12600,
		Done: true, Reported: true, TailBudget: 24000, TailGuard: 36000, Recall: []uint32{2, 36}, Events: []string{"tool"},
	}
	for i, n := range []int{1300, 100, 400, 14200, 1200} {
		s.Tiers = append(s.Tiers, diagnostic.Tier{Index: i, Name: []string{"static", "profile", "recall", "tail", "live"}[i], Tokens: n, Messages: i + 1, Hash: "0123456789abcdef", Chain: "fedcba9876543210"})
	}
	return s
}

func viewer() *model {
	return newModel(termcaps.Caps{Color: termcaps.Color256, Background: termcaps.BgUnknown, Glyphs: termcaps.GlyphsUnicode})
}

func TestOutOfOrderCompletionAndReplayPreserveSelection(t *testing.T) {
	t.Parallel()
	m := viewer()
	first := sample()
	first.Done = false
	m.accept(first)
	second := first
	second.Call, second.Contact, second.Name = 2, 2, "grace"
	m.accept(second)
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.sel != 1 || m.follow {
		t.Fatal("left did not pause on the first call")
	}
	first.Done = true
	m.accept(first)
	if m.sel != 1 || len(m.history) != 2 || !m.history[0].Done {
		t.Fatal("completion of an older call moved selection or duplicated history")
	}
	first.Done = false
	m.accept(first)
	if !m.history[0].Done {
		t.Fatal("a stale replay undid completion")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.sel != 2 || !m.follow {
		t.Fatal("reaching latest did not resume follow")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.filter != 1 || m.sel != 1 {
		t.Fatal("contact switch did not select that contact's history")
	}
	first.Run, first.Call = "server-2", 1
	m.accept(first)
	if len(m.history) != 1 || m.filter != 0 || !m.follow {
		t.Fatal("server restart mixed old and new call identities")
	}
}

func TestHistoryEvictionAndOldSelectedCallRemainUsable(t *testing.T) {
	t.Parallel()
	m := viewer()
	s := sample()
	m.accept(s)
	m.follow = false
	for i := uint64(2); i <= 510; i++ {
		s.Call = i
		m.accept(s)
	}
	if len(m.history) != 500 || m.sel != 11 || m.selected(m.calls()) < 0 {
		t.Fatalf("invalid selection after eviction: %d, length %d", m.sel, len(m.history))
	}
	if strip := ansi.Strip(m.strip(m.history, 0, 20)); len([]rune(strip)) != 20 {
		t.Fatalf("old call not in a bounded strip: %q", strip)
	}
}

func TestLayoutPreservesTiersAndContextCapacity(t *testing.T) {
	t.Parallel()
	s := sample()
	for _, width := range []int{5, 10, 40, 80, 200} {
		for _, span := range []int{s.Total(), s.Window} {
			cells := layout(s, span, width)
			if len(cells) != width {
				t.Fatalf("width %d got %d cells", width, len(cells))
			}
			seen := make(map[int]bool)
			last := 0.0
			for _, c := range cells {
				if c.start < last || c.end < c.start {
					t.Fatal("layout lost monotonic token mapping")
				}
				last = c.end
				if c.tier >= 0 {
					seen[c.tier] = true
				}
			}
			if len(seen) != 5 {
				t.Fatal("minimum cells clipped a nonempty tier")
			}
			if width >= 40 && span == s.Window && cells[width-1].tier != -1 {
				t.Fatal("window view does not show unoccupied capacity")
			}
		}
	}
	if got := reportedPosition(s); math.Abs(got-float64(s.Total())*0.7) > 0.001 {
		t.Fatalf("provider token counts not normalized: %f", got)
	}
	// Even when both markers land in one column, neither is lost: they have
	// their own row. An all-cached prompt must not map past the right edge.
	if c := column(layout(s, s.Total(), 20), float64(s.Total())); c != 19 {
		t.Fatalf("end marker column = %d", c)
	}
}

func TestReportedRoundingDoesNotPaintTheNextTierCached(t *testing.T) {
	t.Parallel()
	s := diagnostic.Snapshot{
		Tiers:    []diagnostic.Tier{{Index: 0, Tokens: 54}, {Index: 3, Tokens: 27}, {Index: 4, Tokens: 60}},
		Reported: true, ReportedInput: 141, ReportedCached: 81,
	}
	m := viewer()
	cells := layout(s, 200000, 80)
	_, fill := m.bar(cells, s)
	if got := ansi.Strip(fill); !strings.HasPrefix(got, "██▒") {
		t.Fatalf("a cache boundary at the start of tier 4 painted tier 4 cached: %q", got)
	}
}

func TestViewFitsAndSanitizesAllDimensions(t *testing.T) {
	t.Parallel()
	for _, ascii := range []bool{false, true} {
		m := viewer()
		if ascii {
			m = newModel(termcaps.Caps{Color: termcaps.ColorNone, Glyphs: termcaps.GlyphsASCII})
		}
		s := sample()
		s.Name = "ada\x1b]52;c;leak\a\n\u202espoof"
		m.accept(s)
		for width := 1; width <= 160; width++ {
			for _, height := range []int{1, 8, 24, 40} {
				m.width, m.height, m.details = width, height, true
				raw := m.View().Content
				if strings.Contains(raw, "\x1b]52") || strings.Contains(raw, "\u202e") {
					t.Fatal("server text escaped into the terminal")
				}
				lines := strings.Split(ansi.Strip(raw), "\n")
				if len(lines) > height {
					t.Fatalf("%d rows in height %d", len(lines), height)
				}
				for _, line := range lines {
					if m.theme.Width(line) > width {
						t.Fatalf("%d-column line in width %d: %q", m.theme.Width(line), width, line)
					}
					if ascii {
						for _, r := range line {
							if r > 127 {
								t.Fatalf("non-ASCII %q", r)
							}
						}
					}
				}
			}
		}
	}
}

func TestContextViewGoldens(t *testing.T) {
	t.Parallel()
	for screen := range 6 {
		m := panelFixture()
		m.screen = screen
		tuitest.Golden(t, fmt.Sprintf("panel-%d.golden", screen+1), []byte(ansi.Strip(m.View().Content)+"\n"))
	}
}

func TestSuccessfulReconnectClearsErrorBeforeFirstCall(t *testing.T) {
	t.Parallel()
	m := viewer()
	m.Update(statusMsg("reconnecting: contactd is not reachable at /tmp/contactd.sock"))
	m.Update(connectedMsg{})
	if m.status != "connected" || len(m.history) != 0 {
		t.Fatalf("idle reconnect: status=%q, calls=%d", m.status, len(m.history))
	}
	view := ansi.Strip(m.View().Content)
	if strings.Contains(view, "not reachable") || !strings.Contains(view, "connected") {
		t.Fatalf("stale startup error after reconnect:\n%s", view)
	}
}
