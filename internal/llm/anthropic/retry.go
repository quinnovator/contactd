package anthropic

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	// maxAttempts bounds one call, counting the first try.
	maxAttempts      = 4
	defaultBaseDelay = 500 * time.Millisecond
	maxBackoff       = 8 * time.Second
	// A longer retry-after than this is not worth holding a turn open for;
	// the ordinary backoff is used instead and the next try may fail fast.
	maxRetryAfter = 30 * time.Second
)

// backoff is the wait before the try after attempt (counted from 0). The
// server's retry-after wins when it is sane; otherwise the delay doubles from
// base, with jitter so contacts throttled together do not retry together.
func backoff(base time.Duration, attempt int, hdr http.Header) time.Duration {
	if d, ok := retryAfter(hdr); ok && d <= maxRetryAfter {
		return d
	}
	d := min(base<<attempt, maxBackoff)
	if d <= 0 {
		return 0
	}
	half := d / 2
	//nolint:gosec // jitter spreads retries; it is not a secret
	return half + rand.N(half+1)
}

// retryAfter reads retry-after-ms or retry-after (seconds or an HTTP date).
func retryAfter(hdr http.Header) (time.Duration, bool) {
	if v := hdr.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	v := hdr.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
		return time.Duration(s * float64(time.Second)), true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(time.Until(at), 0), true
	}
	return 0, false
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("anthropic: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}
