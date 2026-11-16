// Package memlimit derives the Go soft memory limit from the cgroup the
// process runs in. GOMAXPROCS has read the cgroup CPU limit since Go 1.25,
// but GOMEMLIMIT still does not read the memory limit, so a container with a
// 4 GiB cap and no GOMEMLIMIT lets the heap grow until the kernel OOM-kills
// the process instead of collecting harder near the cap.
//
// Detect is pure: it reads files through an fs.FS and an environment lookup,
// so tests can hand it a fake /proc and /sys. The caller applies the result
// with debug.SetMemoryLimit.
package memlimit

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"strconv"
	"strings"
)

// The share of the container limit given to Go, and the least room left for
// memory the Go runtime cannot see. GOMEMLIMIT bounds only what the runtime
// allocates; ONNX Runtime's sessions and arenas, the Rust tokenizer and
// SQLite's page caches (16 MiB per connection, one writer plus GOMAXPROCS
// readers) are allocated by C and count only against the cgroup. That native
// share is roughly fixed rather than proportional to the limit, hence a floor
// as well as a fraction.
const (
	Fraction    = 0.75
	MinHeadroom = 256 << 20
)

// Source says where the limit in force came from.
type Source string

const (
	// SourceEnv: GOMEMLIMIT was set (to anything, "off" included), so the
	// operator's value stands.
	SourceEnv Source = "env"
	// SourceCgroup: the limit was derived from the cgroup memory limit.
	SourceCgroup Source = "cgroup"
	// SourceNone: no GOMEMLIMIT and no cgroup limit, so there is no limit.
	SourceNone Source = "none"
)

// Decision is what Detect found and what the Go limit should be.
type Decision struct {
	Source Source
	// Limit is the Go memory limit to apply, in bytes. Meaningful only for
	// SourceCgroup.
	Limit int64
	// Cgroup is the cgroup memory limit, in bytes, and File the control
	// file it was read from (relative to the fs.FS root). Set only for
	// SourceCgroup.
	Cgroup int64
	File   string
}

// Detect decides the Go memory limit. fsys is rooted at "/" (os.DirFS("/")
// in production). Missing cgroup files mean no limit, not an error: that is
// the normal case outside Linux containers. An error means a control file
// existed but could not be parsed.
func Detect(fsys fs.FS, getenv func(string) string) (Decision, error) {
	if getenv("GOMEMLIMIT") != "" {
		return Decision{Source: SourceEnv}, nil
	}
	limit, file, err := CgroupLimit(fsys)
	if err != nil || limit == 0 {
		return Decision{Source: SourceNone}, err
	}
	return Decision{Source: SourceCgroup, Limit: GoLimit(limit), Cgroup: limit, File: file}, nil
}

// GoLimit is the Go share of a cgroup limit: Fraction of it, leaving at least
// MinHeadroom for native memory, but never less than half the limit, so a
// small container still gets a usable heap.
func GoLimit(cgroup int64) int64 {
	headroom := max(int64(float64(cgroup)*(1-Fraction)), MinHeadroom)
	return max(cgroup-headroom, cgroup/2)
}

// CgroupLimit returns the memory limit of the cgroup this process is in, or
// 0 when there is none, and the control file the limit came from. It tries
// cgroup v2, then v1. Limits on ancestor cgroups apply too (a systemd slice,
// a pod), so it walks from the process's cgroup up to the root of the mount
// and keeps the smallest. Inside a container with its own cgroup namespace
// the path is "/" and only the root file is read.
func CgroupLimit(fsys fs.FS) (int64, string, error) {
	self, err := fs.ReadFile(fsys, "proc/self/cgroup")
	if errors.Is(err, fs.ErrNotExist) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	v2, v1, hasV1 := cgroupPaths(string(self))
	// Without /proc/self/mountinfo, assume the usual mount points with the
	// hierarchy's root mounted.
	mounts := []mount{{point: "/sys/fs/cgroup", root: "/", v2: true}, {point: "/sys/fs/cgroup/memory", root: "/"}}
	if info, err := fs.ReadFile(fsys, "proc/self/mountinfo"); err == nil {
		mounts = cgroupMounts(string(info))
	}
	// On a hybrid host the v2 hierarchy is mounted at /sys/fs/cgroup/unified
	// without the memory controller, so these files are simply absent.
	if m, ok := pick(mounts, true, v2); ok {
		if limit, file, err := walk(fsys, m.point, m.relative(v2), "memory.max", parseV2); err != nil || limit > 0 {
			return limit, file, err
		}
	}
	if m, ok := pick(mounts, false, v1); hasV1 && ok {
		return walk(fsys, m.point, m.relative(v1), "memory.limit_in_bytes", parseV1)
	}
	return 0, "", nil
}

// mount is a cgroup hierarchy mount: where it is mounted, and which cgroup
// of the hierarchy its root is. A container often sees only its own subtree,
// so a process in host cgroup /docker/abc/app may find app at the mount's
// top level.
type mount struct {
	point, root string
	v2          bool
}

// holds reports whether the cgroup is inside the mounted subtree.
func (m mount) holds(cgroup string) bool {
	rel, ok := strings.CutPrefix(path.Clean("/"+cgroup), m.root)
	return m.root == "/" || ok && (rel == "" || rel[0] == '/')
}

// relative maps a cgroup path from /proc/self/cgroup to a path under the
// mount. A path outside the mounted subtree is left as is, and walk then
// skips what it cannot find down to the mount root.
func (m mount) relative(cgroup string) string {
	cgroup = path.Clean("/" + cgroup)
	if m.root == "/" || !m.holds(cgroup) {
		return cgroup
	}
	return path.Clean("/" + strings.TrimPrefix(cgroup, m.root))
}

// cgroupMounts reads /proc/self/mountinfo for cgroup2 mounts and v1 mounts of
// the memory controller. Each line is "id parent dev root point opts...
// - fstype source superopts".
func cgroupMounts(info string) []mount {
	var mounts []mount
	for line := range strings.Lines(info) {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) < 5 || len(g) < 3 {
			continue
		}
		switch {
		case g[0] == "cgroup2":
			mounts = append(mounts, mount{point: f[4], root: f[3], v2: true})
		case g[0] == "cgroup" && containsMemory(g[2]):
			mounts = append(mounts, mount{point: f[4], root: f[3]})
		}
	}
	return mounts
}

// pick returns the first mount of the wanted version whose subtree holds
// the cgroup, or failing that the first of that version.
func pick(mounts []mount, v2 bool, cgroup string) (mount, bool) {
	var (
		first mount
		found bool
	)
	for _, m := range mounts {
		if m.v2 != v2 {
			continue
		}
		if m.holds(cgroup) {
			return m, true
		}
		if !found {
			first, found = m, true
		}
	}
	return first, found
}

// cgroupPaths reads /proc/self/cgroup: "0::/path" is the v2 cgroup, and the
// v1 line whose controller list includes memory gives the v1 one.
func cgroupPaths(self string) (v2, v1 string, hasV1 bool) {
	v2 = "/"
	sc := bufio.NewScanner(strings.NewReader(self))
	for sc.Scan() {
		id, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		controllers, p, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		switch {
		case id == "0" && controllers == "":
			v2 = p
		case containsMemory(controllers):
			v1, hasV1 = p, true
		}
	}
	return v2, v1, hasV1
}

// containsMemory reports whether a comma-separated controller or option
// list names the memory controller.
func containsMemory(controllers string) bool {
	for c := range strings.SplitSeq(strings.TrimSpace(controllers), ",") {
		if c == "memory" {
			return true
		}
	}
	return false
}

// walk reads name in mount/dir and each of its ancestors up to mount itself,
// returning the smallest limit found. Directories that do not exist under the
// mount (a v1 container sees its own cgroup as the root while
// /proc/self/cgroup still names the host path) are skipped.
func walk(fsys fs.FS, mount, dir, name string, parse func(string) (int64, error)) (int64, string, error) {
	var (
		best     int64
		bestFile string
	)
	// fs.FS paths are unrooted; path.Clean on a rooted path also drops any
	// leading "..".
	mount = strings.TrimPrefix(path.Clean("/"+mount), "/")
	dir = path.Clean("/" + dir)
	for {
		file := path.Join(mount, dir, name)
		raw, err := fs.ReadFile(fsys, file)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return 0, "", fmt.Errorf("read /%s: %w", file, err)
		default:
			limit, err := parse(strings.TrimSpace(string(raw)))
			if err != nil {
				return 0, "", fmt.Errorf("parse /%s: %w", file, err)
			}
			if limit > 0 && (best == 0 || limit < best) {
				best, bestFile = limit, "/"+file
			}
		}
		if dir == "/" {
			return best, bestFile, nil
		}
		dir = path.Dir(dir)
	}
}

// parseV2 reads memory.max: a byte count, or "max" for no limit.
func parseV2(s string) (int64, error) {
	if s == "max" {
		return 0, nil
	}
	return parseBytes(s)
}

// v1 has no "max": an unlimited cgroup reports the largest page-aligned
// int64 (9223372036854771712 with 4 KiB pages), so anything this big means
// no limit.
const v1Unlimited = 1 << 62

func parseV1(s string) (int64, error) {
	n, err := parseBytes(s)
	if err != nil || n >= v1Unlimited {
		return 0, err
	}
	return n, nil
}

func parseBytes(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	switch {
	case errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(s, "-"):
		return math.MaxInt64, nil
	case err != nil:
		return 0, fmt.Errorf("not a byte count: %w", err)
	case n < 0:
		return 0, fmt.Errorf("not a byte count: %d", n)
	}
	return n, nil
}
