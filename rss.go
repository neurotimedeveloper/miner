package miner

import (
	"runtime"
	"syscall"
)

// peakRSS returns the process's peak resident set in bytes, as the kernel
// reports it. The acceptance criterion is measured from outside via cgroup;
// this is the number the run reports about itself.
func peakRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	// Linux reports kilobytes, Darwin bytes.
	if runtime.GOOS == "linux" {
		return int64(ru.Maxrss) * 1024
	}
	return int64(ru.Maxrss)
}
