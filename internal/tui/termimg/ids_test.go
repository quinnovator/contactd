package termimg_test

import (
	"slices"
	"strconv"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/termimg"
)

func TestIDsBase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		base int
		want []int // the first three ids
	}{
		{base: 0, want: []int{1, 2, 3}},
		{base: 1, want: []int{2, 3, 4}},
		{base: 253, want: []int{254, 255, 1}},
		{base: 254, want: []int{255, 1, 2}},
		{base: 255, want: []int{1, 2, 3}},
		{base: 1_760_000_000, want: []int{1_760_000_000%255 + 1, (1_760_000_000+1)%255 + 1, (1_760_000_000+2)%255 + 1}},
		{base: -1, want: []int{255, 1, 2}},
		{base: -256, want: []int{255, 1, 2}},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.base), func(t *testing.T) {
			t.Parallel()
			ids := termimg.NewIDs(c.base)
			got := make([]int, 0, 3)
			for i := range 3 {
				id, evicted, fresh := ids.Get("k" + strconv.Itoa(i))
				if evicted != "" || !fresh {
					t.Errorf("Get of a new key = evicted %q, fresh %v; want none, true", evicted, fresh)
				}
				got = append(got, id)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("first ids = %v, want %v", got, c.want)
			}
		})
	}
}

func TestIDsReuse(t *testing.T) {
	t.Parallel()
	ids := termimg.NewIDs(40)
	if got := ids.Live(); len(got) != 0 {
		t.Errorf("Live of a new pool = %v, want none", got)
	}
	a, _, _ := ids.Get("31")
	b, _, _ := ids.Get("31.2")
	if a == b {
		t.Fatalf("two keys share id %d", a)
	}
	for range 3 {
		id, evicted, fresh := ids.Get("31")
		if id != a || evicted != "" || fresh {
			t.Errorf("Get of a key that has an id = %d, %q, %v; want %d, none, false", id, evicted, fresh, a)
		}
	}
	if got, want := ids.Live(), []int{41, 42}; !slices.Equal(got, want) {
		t.Errorf("Live = %v, want %v", got, want)
	}
}

func TestIDsEviction(t *testing.T) {
	t.Parallel()
	ids := termimg.NewIDs(100)
	key := func(i int) string { return "k" + strconv.Itoa(i) }
	owner := make(map[int]string)
	for i := range 255 {
		id, evicted, fresh := ids.Get(key(i))
		if id < 1 || id > 255 || evicted != "" || !fresh {
			t.Fatalf("Get(%s) = %d, %q, %v; want an id of 1 to 255, none, true", key(i), id, evicted, fresh)
		}
		if other, taken := owner[id]; taken {
			t.Fatalf("id %d given to %s and to %s", id, other, key(i))
		}
		owner[id] = key(i)
	}
	live := ids.Live()
	if len(live) != 255 || live[0] != 1 || live[254] != 255 || !slices.IsSorted(live) {
		t.Fatalf("Live = %v, want 1 to 255 in order", live)
	}

	// k0 is asked for again, so k1 is now the one asked for longest ago.
	k0, _, _ := ids.Get(key(0))
	k1 := 0
	for id, k := range owner {
		if k == key(1) {
			k1 = id
		}
	}
	id, evicted, fresh := ids.Get("new")
	if id != k1 || evicted != key(1) || !fresh {
		t.Errorf("Get past 255 = %d, %q, %v; want k1's id %d, %q, true", id, evicted, fresh, k1, key(1))
	}
	if got, _, fresh := ids.Get(key(0)); got != k0 || fresh {
		t.Errorf("k0 lost its id: got %d, fresh %v; want %d", got, fresh, k0)
	}
	if got, _, fresh := ids.Get("new"); got != k1 || fresh {
		t.Errorf("the new key's id changed: got %d, fresh %v; want %d", got, fresh, k1)
	}

	// The key that lost its id gets another when it comes back: k2's,
	// which is now the oldest.
	id, evicted, fresh = ids.Get(key(1))
	if evicted != key(2) || !fresh || id == k1 {
		t.Errorf("Get of the evicted key = %d, %q, %v; want another id, %q, true", id, evicted, fresh, key(2))
	}
	if got := ids.Live(); len(got) != 255 {
		t.Errorf("Live holds %d ids, want 255", len(got))
	}
}
