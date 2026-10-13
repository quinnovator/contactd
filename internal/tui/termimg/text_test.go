package termimg_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/quinnovator/contactd/internal/tui/termimg"
	"github.com/quinnovator/contactd/internal/tui/tuitest"
)

func TestTextLine(t *testing.T) {
	t.Parallel()
	picture := termimg.Info{Format: "png", W: 640, H: 360}
	cases := []struct {
		name  string
		file  string
		info  termimg.Info
		bytes int64
		want  string // with Unicode glyphs
		ascii string // with ASCII glyphs
	}{
		{
			name: "the mock-up's line", file: "latency.png", info: picture, bytes: 18_204,
			want: "[image: latency.png, 640×360, 18 kB]", ascii: "[image: latency.png, 640x360, 18 kB]",
		},
		{
			name: "an uncompressed 640x360 PNG", file: "latency.png", info: picture, bytes: 691_873,
			want: "[image: latency.png, 640×360, 692 kB]", ascii: "[image: latency.png, 640x360, 692 kB]",
		},
		{name: "still loading", file: "latency.png", want: "[image: latency.png]", ascii: "[image: latency.png]"},
		{
			name: "a size and no picture yet", file: "a.gif", bytes: 512,
			want: "[image: a.gif, 512 B]", ascii: "[image: a.gif, 512 B]",
		},
		{
			name: "under a kilobyte", file: "dot.gif", info: termimg.Info{W: 1, H: 1}, bytes: 999,
			want: "[image: dot.gif, 1×1, 999 B]", ascii: "[image: dot.gif, 1x1, 999 B]",
		},
		{
			name: "a kilobyte is 1000 bytes", file: "a.png", info: picture, bytes: 1000,
			want: "[image: a.png, 640×360, 1 kB]", ascii: "[image: a.png, 640x360, 1 kB]",
		},
		{
			name: "rounded to the nearest kilobyte", file: "a.png", info: picture, bytes: 1500,
			want: "[image: a.png, 640×360, 2 kB]", ascii: "[image: a.png, 640x360, 2 kB]",
		},
		{
			name: "the last size in kilobytes", file: "a.png", info: picture, bytes: 999_499,
			want: "[image: a.png, 640×360, 999 kB]", ascii: "[image: a.png, 640x360, 999 kB]",
		},
		{
			name: "the first in megabytes", file: "a.png", info: picture, bytes: 999_500,
			want: "[image: a.png, 640×360, 1.0 MB]", ascii: "[image: a.png, 640x360, 1.0 MB]",
		},
		{
			name: "the largest file fetched", file: "photo.jpeg", info: termimg.Info{W: 4000, H: 3000}, bytes: 8 << 20,
			want: "[image: photo.jpeg, 4000×3000, 8.4 MB]", ascii: "[image: photo.jpeg, 4000x3000, 8.4 MB]",
		},
		{
			name: "a name that is not ASCII", file: "график.png", info: picture, bytes: 2048,
			want: "[image: график.png, 640×360, 2 kB]", ascii: "[image: ??????.png, 640x360, 2 kB]",
		},
		{
			name: "a name with escape sequences in it", file: "a\x1b]0;owned\x07b\x1b[31m.png", info: picture, bytes: 2048,
			want: "[image: a]0;ownedb[31m.png, 640×360, 2 kB]", ascii: "[image: a]0;ownedb[31m.png, 640x360, 2 kB]",
		},
		{
			name: "a name made of image placeholders", file: "\U0010EEEE\u0305\u0305x.png",
			want: "[image: \u0305\u0305x.png]", ascii: "[image: ??x.png]",
		},
		{
			name: "a name on two lines", file: "a\nb.png",
			want: "[image: a b.png]", ascii: "[image: a b.png]",
		},
		{name: "no name", file: "", want: "[image: file]", ascii: "[image: file]"},
		{name: "a name of control characters only", file: "\x1b\x07", want: "[image: file]", ascii: "[image: file]"},
	}
	var golden strings.Builder
	for _, c := range cases {
		golden.WriteString("# " + c.name + "\n")
		golden.WriteString(termimg.TextLine(c.file, c.info, c.bytes, false) + "\n")
		golden.WriteString(termimg.TextLine(c.file, c.info, c.bytes, true) + "\n\n")
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := termimg.TextLine(c.file, c.info, c.bytes, false); got != c.want {
				t.Errorf("unicode: %q, want %q", got, c.want)
			}
			got := termimg.TextLine(c.file, c.info, c.bytes, true)
			if got != c.ascii {
				t.Errorf("ascii: %q, want %q", got, c.ascii)
			}
			for i := range len(got) {
				if got[i] >= 0x80 || got[i] < 0x20 {
					t.Errorf("ascii: byte %d of %q is %#x", i, got, got[i])
				}
			}
		})
	}
	tuitest.Golden(t, "textline.golden", []byte(golden.String()))
}

func TestTextLineLongName(t *testing.T) {
	t.Parallel()
	got := termimg.TextLine(strings.Repeat("é", 500)+".png", termimg.Info{}, 0, false)
	if n := utf8.RuneCountInString(got); n != len("[image: ]")+80 {
		t.Errorf("the line has %d runes, want the name cut to 80: %q", n, got)
	}
}
