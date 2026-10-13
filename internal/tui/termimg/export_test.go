package termimg

import (
	"context"
	"image"
)

// DecodeContext is Decode that gives up when ctx ends, as a job decodes.
func DecodeContext(ctx context.Context, data []byte, l Limits) (image.Image, Info, error) {
	return decode(ctx, data, l)
}

// CacheBytes returns how many bytes of masters the store holds.
func (s *Store) CacheBytes() int { return s.cache.size }

// CacheLen returns how many masters the store holds.
func (s *Store) CacheLen() int { return len(s.cache.items) }
