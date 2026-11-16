package main

import (
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/quinnovator/contactd/internal/memlimit"
)

// applyMemoryLimit sets the Go soft memory limit from the cgroup memory
// limit when GOMEMLIMIT is unset, logs what is in force, and returns it as
// debug.SetMemoryLimit reports it (math.MaxInt64 for no limit).
func applyMemoryLimit(log *slog.Logger) int64 {
	d, err := memlimit.Detect(os.DirFS("/"), os.Getenv)
	if err != nil {
		log.Warn("could not read the cgroup memory limit; GOMEMLIMIT left unset", "error", err)
	}
	switch d.Source {
	case memlimit.SourceCgroup:
		debug.SetMemoryLimit(d.Limit)
		log.Info("GOMEMLIMIT set from the cgroup memory limit",
			"gomemlimit", d.Limit,
			"cgroup_limit", d.Cgroup,
			"cgroup_file", d.File)
	case memlimit.SourceEnv:
		log.Info("GOMEMLIMIT set by the environment", "gomemlimit", debug.SetMemoryLimit(-1))
	case memlimit.SourceNone:
		if err == nil {
			log.Info("no GOMEMLIMIT and no cgroup memory limit; the Go heap is unbounded")
		}
	}
	return debug.SetMemoryLimit(-1)
}
