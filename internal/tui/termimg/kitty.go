package termimg

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

const (
	// maxID is the largest image id. An id travels to the terminal as a
	// colour of the 256-colour palette, the only kind of colour the cell
	// renderer passes on unchanged, so it has eight bits; kitty reads zero
	// as "no image".
	maxID = 255
	// maxGrid is the most columns or rows a placement can have: the
	// protocol's table of row and column marks has 297 entries.
	maxGrid = 297
)

// KittyTransmit returns the bytes that hand a picture to the terminal and
// place it, unseen, on a grid of cols by rows cells. Nothing shows until the
// cells of KittyPlaceholders are drawn. The terminal stretches the picture to
// the grid, so img should already have the grid's shape.
//
// The picture travels as a PNG the client encoded itself, base64 in chunks
// of 4096 bytes, one kitty graphics command a chunk. The first command
// carries the keys (a=T,U=1,q=2,f=100,t=d,i,c,r) and every command says with
// m whether more follow. q=2 asks the terminal not to answer. With tmux each
// command is wrapped in tmux's passthrough sequence.
//
// The bytes are for the terminal alone: send them outside the view. Sending
// them again with the same id replaces the picture.
//
// It is an error when id is not 1 to 255, when cols or rows is not 1 to 297,
// and when img is empty.
func KittyTransmit(id int, img *image.RGBA, cols, rows int, tmux bool) (string, error) {
	if id < 1 || id > maxID {
		return "", fmt.Errorf("termimg: image id %d is not 1 to %d", id, maxID)
	}
	if cols < 1 || cols > maxGrid || rows < 1 || rows > maxGrid {
		return "", fmt.Errorf("termimg: a grid of %dx%d cells is not 1 to %d a side", cols, rows, maxGrid)
	}
	if img == nil || img.Bounds().Empty() {
		return "", errors.New("termimg: the picture is empty")
	}
	var file bytes.Buffer
	if err := png.Encode(&file, img); err != nil {
		return "", fmt.Errorf("termimg: encode the picture: %w", err)
	}
	payload := make([]byte, base64.StdEncoding.EncodedLen(file.Len()))
	base64.StdEncoding.Encode(payload, file.Bytes())

	var b strings.Builder
	for first := true; len(payload) > 0; first = false {
		chunk := payload[:min(kitty.MaxChunkSize, len(payload))]
		payload = payload[len(chunk):]
		more := "m=0"
		if len(payload) > 0 {
			more = "m=1"
		}
		opts := []string{more}
		if first {
			opts = []string{
				"a=T", "U=1", "q=2", "f=100", "t=d",
				"i=" + strconv.Itoa(id), "c=" + strconv.Itoa(cols), "r=" + strconv.Itoa(rows), more,
			}
		}
		b.WriteString(passthrough(ansi.KittyGraphics(chunk, opts...), tmux))
	}
	return b.String(), nil
}

// KittyPlaceholders returns the rows of cells that show picture id on a grid
// of cols by rows. Each row sets the foreground to colour id of the
// 256-colour palette, which is how the terminal learns the id; then holds one
// placeholder character (U+10EEEE) per cell, marked with its row and its
// column, so a cell means the same whichever of its neighbours is redrawn;
// then puts the foreground back. Each cell is one column wide. The rows hold
// no graphics command, so they are safe in a view.
//
// It returns nil when id is not 1 to 255 or the grid is not 1 to 297 a side.
func KittyPlaceholders(id, cols, rows int) []string {
	if id < 1 || id > maxID || cols < 1 || cols > maxGrid || rows < 1 || rows > maxGrid {
		return nil
	}
	out := make([]string, rows)
	start := "\x1b[38;5;" + strconv.Itoa(id) + "m"
	const end = "\x1b[39m"
	var b strings.Builder
	for y := range out {
		b.Reset()
		// A placeholder is four bytes; a mark is two or three.
		b.Grow(len(start) + cols*10 + len(end))
		b.WriteString(start)
		for x := range cols {
			b.WriteRune(kitty.Placeholder)
			b.WriteRune(kitty.Diacritic(y))
			b.WriteRune(kitty.Diacritic(x))
		}
		b.WriteString(end)
		out[y] = b.String()
	}
	return out
}

// KittyDelete returns the bytes that make the terminal forget picture id: it
// removes every cell that shows it and frees its data (a=d,d=I,i,q=2). With
// tmux the command is wrapped in tmux's passthrough sequence. It returns ""
// when id is not 1 to 255.
func KittyDelete(id int, tmux bool) string {
	if id < 1 || id > maxID {
		return ""
	}
	return passthrough(ansi.KittyGraphics(nil, "a=d", "d=I", "i="+strconv.Itoa(id), "q=2"), tmux)
}

// passthrough wraps a command for the terminal behind tmux, or returns it as
// it is.
func passthrough(seq string, tmux bool) string {
	if tmux {
		return ansi.TmuxPassthrough(seq)
	}
	return seq
}
