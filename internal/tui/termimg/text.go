package termimg

import (
	"strconv"
	"strings"

	"github.com/quinnovator/contactd/internal/tui/safe"
)

// maxName is the most runes of a file name a line shows.
const maxName = 80

// TextLine returns the one line that stands for an image where no picture
// is drawn: "[image: latency.png, 640×360, 692 kB]". The size in pixels is
// left out while in has none, and the size of the file while bytes is zero,
// so an image still being fetched is "[image: latency.png]".
//
// The name is made safe to draw: it comes from a reply. With ascii the line
// holds no byte above 0x7F: the times sign is an "x" and any other such
// character in the name is a "?".
func TextLine(name string, in Info, bytes int64, ascii bool) string {
	return "[image: " + strings.Join(facts(name, in, bytes, ascii), ", ") + "]"
}

// facts returns what is known about an image, in the order every line gives
// it: the name, the size in pixels, the size of the file.
func facts(name string, in Info, bytes int64, ascii bool) []string {
	out := make([]string, 0, 5)
	out = append(out, cleanName(name, ascii))
	if in.W > 0 && in.H > 0 {
		times := "×"
		if ascii {
			times = "x"
		}
		out = append(out, strconv.Itoa(in.W)+times+strconv.Itoa(in.H))
	}
	if bytes > 0 {
		out = append(out, sizeText(bytes))
	}
	return out
}

// cleanName makes a file name safe to draw on one line.
func cleanName(name string, ascii bool) string {
	if name = safe.Line(name, maxName, ascii); name != "" {
		return name
	}
	return "file"
}

// sizeText writes a number of bytes the way the lines do: "512 B", "692 kB",
// "1.4 MB". The units are decimal.
func sizeText(bytes int64) string {
	switch {
	case bytes < 1000:
		return strconv.FormatInt(bytes, 10) + " B"
	case bytes < 999_500:
		return strconv.FormatInt((bytes+500)/1000, 10) + " kB"
	}
	tenths := (bytes + 50_000) / 100_000
	return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10) + " MB"
}

// failure is why an image cannot be shown.
type failure uint8

const (
	// failNotFound means the server has no such file.
	failNotFound failure = iota + 1
	// failTooLarge means the file or its picture is over a limit.
	failTooLarge
	// failFormat means the file is not a PNG, a JPEG or a GIF.
	failFormat
	// failUnreadable means anything else: the fetch failed, or the file's
	// body could not be decoded. The file may have been half written.
	failUnreadable
)

// failLine returns the one line shown for an image that failed:
// "[image: latency.png, not found]".
func failLine(name string, why failure, ascii bool) string {
	reason := ""
	switch why {
	case failNotFound:
		reason = "not found"
	case failTooLarge:
		reason = "too large"
	case failFormat:
		reason = "not an image"
	case failUnreadable:
		reason = "could not be read; /reload to retry"
	}
	return "[image: " + cleanName(name, ascii) + ", " + reason + "]"
}

// caption returns the line under a picture:
// "latency.png · 640×360 · 692 kB · /image save 31", and " · changed since"
// when the file is newer than the message that named it. number is the
// image's number as /image takes it.
func caption(name string, in Info, bytes int64, number string, changed, ascii bool) string {
	sep := " · "
	if ascii {
		sep = " | "
	}
	parts := append(facts(name, in, bytes, ascii), "/image save "+number)
	if changed {
		parts = append(parts, "changed since")
	}
	return strings.Join(parts, sep)
}

// saveHint returns the command that saves the file beside the user:
// "/image save 31 ./latency.png". A name with a space is quoted, as the
// command line reads it.
func saveHint(name, number string, ascii bool) string {
	to := "./" + cleanName(name, ascii)
	if strings.Contains(to, " ") {
		to = `"` + to + `"`
	}
	return "/image save " + number + " " + to
}
