package draw

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// ContentMetrics describes text without highlighting it, invoking a mod, or
// fetching an image. Rows includes wrapping at the supplied terminal width.
type ContentMetrics struct {
	MinCols, PreferredCols int
	Rows                   func(cols int) int
}

// CodeMetrics keeps indentation and long lines in the width calculation.
func (r *Renderer) CodeMetrics(lang, src string) ContentMetrics {
	key := "code:" + lang + "\x00" + src
	if found, ok := r.metrics[key]; ok {
		return found
	}
	lines := strings.Split(strings.Trim(r.clean(src), "\n"), "\n")
	widths := make([]int, len(lines))
	preferred := r.th.Width(r.name(lang)) + 4
	for i, line := range lines {
		widths[i] = r.th.Width(line)
		preferred = max(preferred, widths[i]+4)
	}
	return r.keepMetrics(key, ContentMetrics{
		MinCols: min(max(preferred, 4), 28), PreferredCols: max(preferred, 4),
		Rows: func(cols int) int {
			rows := 0
			if lang != "" {
				rows++
			}
			for _, width := range widths {
				rows += max((width+max(cols-4, 1)-1)/max(cols-4, 1), 1)
			}
			return rows
		},
	})
}

// TableMetrics uses the same table grammar as the Markdown renderer. Pipes
// inside inline code, escaped pipes, emphasis and links are measured as cells,
// rather than counted as literal Markdown source.
func (r *Renderer) TableMetrics(src string) ContentMetrics {
	key := "table:" + src
	if found, ok := r.metrics[key]; ok {
		return found
	}
	raw := []byte(r.clean(src))
	doc := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(raw))
	var widths []int
	rows := 0
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if n.Kind() != extast.KindTableRow && n.Kind() != extast.KindTableHeader {
			return ast.WalkContinue, nil
		}
		rows++
		i := 0
		for cell := n.FirstChild(); cell != nil; cell = cell.NextSibling() {
			if i == len(widths) {
				widths = append(widths, 1)
			}
			var words strings.Builder
			_ = ast.Walk(cell, func(part ast.Node, enter bool) (ast.WalkStatus, error) {
				if enter {
					switch value := part.(type) {
					case *ast.Text:
						v := value.Segment.Value(raw)
						if !value.IsRaw() {
							v = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(v)))
						}
						words.Write(v)
					case *ast.String:
						words.Write(value.Value)
					case *ast.AutoLink:
						words.Write(value.Label(raw))
					}
				}
				return ast.WalkContinue, nil
			})
			widths[i] = max(widths[i], r.th.Width(words.String()))
			i++
		}
		return ast.WalkContinue, nil
	})
	if len(widths) == 0 {
		return r.CodeMetrics("", src)
	}
	preferred, minimum := indent, indent
	for _, width := range widths {
		preferred += width + 3
		minimum += min(width, 8) + 3
	}
	return r.keepMetrics(key, ContentMetrics{
		MinCols: minimum, PreferredCols: preferred,
		Rows: func(cols int) int {
			// Under the natural width, the renderer must wrap. Penalize
			// such candidates rather than quietly truncating table cells.
			return (rows + 1) * max((preferred+max(cols, 1)-1)/max(cols, 1), 1)
		},
	})
}

func (r *Renderer) keepMetrics(key string, value ContentMetrics) ContentMetrics {
	const maxMeasurements = 32
	if r.metrics == nil || len(r.metrics) >= maxMeasurements {
		r.metrics = make(map[string]ContentMetrics)
	}
	r.metrics[key] = value
	return value
}
