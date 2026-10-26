package termimg_test

import (
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termcaps"
	"github.com/quinnovator/contactd/internal/tui/termimg"
)

func TestMeasuringImagesDoesNotScheduleOrChangePlacement(t *testing.T) {
	t.Parallel()
	s := newStore(termcaps.ImagesPixels)
	if _, _, known := s.Measure(latency(), 40, 10); known {
		t.Fatal("an unknown image has no measured size")
	}
	idle(t, s)
	srv, _ := withChart(t)
	s.Show(latency(), 44, 12)
	settle(t, s, srv)
	before := s.Show(latency(), 44, 12)
	stats := s.Stats()
	for _, size := range [][2]int{{44, 12}, {20, 4}, {8, 8}, {100, 100}} {
		w, h, known := s.Measure(latency(), size[0], size[1])
		if !known || w < 1 || h < 1 || w > size[0] || h > size[1] {
			t.Fatalf("invalid fit for %v: %d×%d known=%v", size, w, h, known)
		}
	}
	idle(t, s)
	if !sameShown(before, s.Show(latency(), 44, 12)) || s.Stats() != stats {
		t.Fatal("speculative measurement changed the image or transmitted pixels")
	}
}

func TestStageImagesCanUseMoreThanAThumbnail(t *testing.T) {
	t.Parallel()
	s := termimg.NewStore(termimg.StoreOptions{MaxCols: 120, MaxRows: 40})
	s.SetCaps(caps(termcaps.ImagesBlocks))
	srv, _ := withChart(t)
	s.Show(latency(), 100, 30)
	settle(t, s, srv)
	shown := s.Show(latency(), 100, 30)
	w, h, known := s.Measure(latency(), 100, 30)
	if !known || w <= 44 || h <= 12 || len(shown.Rows) != h {
		t.Fatalf("larger stage still got a thumbnail: %d×%d, %d rows", w, h, len(shown.Rows))
	}
	if s.CacheBytes() > 16<<20 {
		t.Fatal("larger image escaped the cache budget")
	}
}
