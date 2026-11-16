package memlimit_test

import (
	"testing"
	"testing/fstest"

	"github.com/quinnovator/contactd/internal/memlimit"
)

const (
	mib = int64(1) << 20
	gib = int64(1) << 30
)

func noEnv(string) string { return "" }

func files(pairs ...string) fstest.MapFS {
	m := fstest.MapFS{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = &fstest.MapFile{Data: []byte(pairs[i+1])}
	}
	return m
}

func TestGoLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cgroup, want int64
	}{
		// The README's own example: a 4 GiB container runs with 3 GiB.
		{4 * gib, 3 * gib},
		{16 * gib, 12 * gib},
		// Below 1 GiB the 256 MiB floor wins over a quarter.
		{1 * gib, 768 * mib},
		{768 * mib, 512 * mib},
		// ...until Go would get less than half.
		{512 * mib, 256 * mib},
		{256 * mib, 128 * mib},
	} {
		if got := memlimit.GoLimit(tc.cgroup); got != tc.want {
			t.Errorf("GoLimit(%d MiB) = %d MiB, want %d MiB", tc.cgroup/mib, got/mib, tc.want/mib)
		}
	}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		fsys     fstest.MapFS
		env      func(string) string
		source   memlimit.Source
		cgroup   int64
		file     string
		wantsErr bool
	}{
		{
			name: "v2 container with its own cgroup namespace",
			fsys: files(
				"proc/self/cgroup", "0::/\n",
				"sys/fs/cgroup/memory.max", "4294967296\n",
			),
			source: memlimit.SourceCgroup, cgroup: 4 * gib, file: "/sys/fs/cgroup/memory.max",
		},
		{
			name: "v2 with no limit",
			fsys: files(
				"proc/self/cgroup", "0::/\n",
				"sys/fs/cgroup/memory.max", "max\n",
			),
			source: memlimit.SourceNone,
		},
		{
			name: "v2 systemd unit: the tighter of the unit and its slice",
			fsys: files(
				"proc/self/cgroup", "0::/system.slice/contactd.service\n",
				"sys/fs/cgroup/system.slice/contactd.service/memory.max", "8589934592\n",
				"sys/fs/cgroup/system.slice/memory.max", "2147483648\n",
			),
			source: memlimit.SourceCgroup, cgroup: 2 * gib, file: "/sys/fs/cgroup/system.slice/memory.max",
		},
		{
			name: "v1 container sees its cgroup as the mount root",
			fsys: files(
				"proc/self/cgroup", "12:pids:/docker/abc\n11:cpu,cpuacct:/docker/abc\n4:memory:/docker/abc\n0::/system.slice/containerd.service\n",
				"sys/fs/cgroup/memory/memory.limit_in_bytes", "1073741824\n",
			),
			source: memlimit.SourceCgroup, cgroup: 1 * gib, file: "/sys/fs/cgroup/memory/memory.limit_in_bytes",
		},
		{
			name: "v1 with no limit reports a page-aligned MaxInt64",
			fsys: files(
				"proc/self/cgroup", "4:memory:/\n",
				"sys/fs/cgroup/memory/memory.limit_in_bytes", "9223372036854771712\n",
			),
			source: memlimit.SourceNone,
		},
		{
			name: "v1 value past MaxInt64 still means no limit",
			fsys: files(
				"proc/self/cgroup", "4:memory:/\n",
				"sys/fs/cgroup/memory/memory.limit_in_bytes", "18446744073709551615\n",
			),
			source: memlimit.SourceNone,
		},
		{
			name: "hybrid host: memory is on v1, v2 has no memory.max",
			fsys: files(
				"proc/self/cgroup", "5:memory:/user.slice\n0::/user.slice\n",
				"sys/fs/cgroup/unified/user.slice/cgroup.procs", "1\n",
				"sys/fs/cgroup/memory/user.slice/memory.limit_in_bytes", "3221225472\n",
			),
			source: memlimit.SourceCgroup, cgroup: 3 * gib, file: "/sys/fs/cgroup/memory/user.slice/memory.limit_in_bytes",
		},
		{
			name:   "not Linux: no /proc at all",
			fsys:   files(),
			source: memlimit.SourceNone,
		},
		{
			name: "GOMEMLIMIT set wins, even with a cgroup limit",
			fsys: files(
				"proc/self/cgroup", "0::/\n",
				"sys/fs/cgroup/memory.max", "4294967296\n",
			),
			env:    func(k string) string { return map[string]string{"GOMEMLIMIT": "2GiB"}[k] },
			source: memlimit.SourceEnv,
		},
		{
			name: "GOMEMLIMIT=off is respected too",
			fsys: files(
				"proc/self/cgroup", "0::/\n",
				"sys/fs/cgroup/memory.max", "4294967296\n",
			),
			env:    func(k string) string { return map[string]string{"GOMEMLIMIT": "off"}[k] },
			source: memlimit.SourceEnv,
		},
		{
			name: "garbage in a control file is an error, and no limit",
			fsys: files(
				"proc/self/cgroup", "0::/\n",
				"sys/fs/cgroup/memory.max", "lots\n",
			),
			source: memlimit.SourceNone, wantsErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := tc.env
			if env == nil {
				env = noEnv
			}
			d, err := memlimit.Detect(tc.fsys, env)
			if (err != nil) != tc.wantsErr {
				t.Fatalf("err = %v, wantsErr %v", err, tc.wantsErr)
			}
			if d.Source != tc.source || d.Cgroup != tc.cgroup || d.File != tc.file {
				t.Fatalf("got %+v, want source %s cgroup %d file %s", d, tc.source, tc.cgroup, tc.file)
			}
			if d.Source == memlimit.SourceCgroup && d.Limit != memlimit.GoLimit(d.Cgroup) {
				t.Errorf("limit %d, want GoLimit(%d) = %d", d.Limit, d.Cgroup, memlimit.GoLimit(d.Cgroup))
			}
		})
	}
}
