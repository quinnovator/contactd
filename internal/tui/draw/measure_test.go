package draw_test

import (
	"testing"

	"github.com/quinnovator/contactd/internal/tui/draw"
	"github.com/quinnovator/contactd/internal/tui/theme"
)

func TestCodeMetricsCountCellsAndWrapping(t *testing.T) {
	t.Parallel()
	r := draw.New(draw.Options{Theme: theme.New(trueDark().caps, theme.Options{}), Width: 100})
	m := r.CodeMetrics("go", "世界世界\nx := 1")
	if m.PreferredCols != 12 || m.Rows(12) != 3 || m.Rows(8) != 5 {
		t.Fatalf("unexpected code geometry: preferred=%d rows(12)=%d rows(8)=%d", m.PreferredCols, m.Rows(12), m.Rows(8))
	}
}

func TestTableMetricsUseRenderedCellText(t *testing.T) {
	t.Parallel()
	r := draw.New(draw.Options{Theme: theme.New(trueDark().caps, theme.Options{}), Width: 100})
	plain := r.TableMetrics("| 状态 | Link |\n|---|---|\n| 正常 | API |")
	styled := r.TableMetrics("| **状态** | Link |\n|---|---|\n| *正常* | [API](https://example.com/a/very/long/url) |")
	if styled.PreferredCols != plain.PreferredCols || styled.Rows(100) != 3 {
		t.Fatalf("Markdown syntax changed the measured table: plain=%+v styled=%+v", plain, styled)
	}
	if styled.Rows(10) <= styled.Rows(100) {
		t.Fatal("narrow columns must account for wrapping")
	}
}

func TestTableMetricsMeasureEscapesAndEntities(t *testing.T) {
	t.Parallel()
	r := draw.New(draw.Options{Theme: theme.New(trueDark().caps, theme.Options{}), Width: 100})
	for _, src := range []string{"| A\\|B |\n|---|\n| x |", "| A&amp;B |\n|---|\n| x |", "| `A&B` |\n|---|\n| x |"} {
		if got := r.TableMetrics(src).PreferredCols; got != 8 {
			t.Errorf("preferred width for %q = %d, want 8", src, got)
		}
	}
}
