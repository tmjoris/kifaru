package main

import (
	"log"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// tuneRuntime sizes the Go runtime for the container it is actually in.
//
// On Render's free web service the process gets about a tenth of a CPU and
// 512 MB. Without help, Go sees every core of the host: it starts too many
// threads, which the CPU quota then throttles in bursts, and its garbage
// collector only runs when the heap has doubled, which on 512 MB ends in an
// out-of-memory kill. Anything set through the environment (GOMAXPROCS,
// GOMEMLIMIT, GOGC) wins over what is worked out here.
func tuneRuntime() {
	notes := []string{}
	if os.Getenv("GOMAXPROCS") == "" {
		if cpus := cgroupCPUs(); cpus > 0 {
			procs := max(1, int(math.Ceil(cpus)))
			runtime.GOMAXPROCS(procs)
			notes = append(notes, "GOMAXPROCS="+strconv.Itoa(procs)+" (cpu quota "+strconv.FormatFloat(cpus, 'f', 2, 64)+")")
		}
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		if limit := cgroupMemoryBytes(); limit > 0 {
			soft := limit / 100 * 75
			debug.SetMemoryLimit(soft)
			notes = append(notes, "memory limit "+strconv.FormatInt(soft>>20, 10)+" MiB of "+strconv.FormatInt(limit>>20, 10))
		}
	}
	if os.Getenv("GOGC") == "" {
		// The heap is small and a collection costs CPU we do not have, so let
		// it grow further between collections; the memory limit above is the safety net.
		debug.SetGCPercent(200)
		notes = append(notes, "GOGC=200")
	}
	if len(notes) > 0 {
		log.Printf("runtime tuned: %s", strings.Join(notes, ", "))
	}
}

// cgroupCPUs returns the CPU quota in cores, or 0 when there is none.
func cgroupCPUs() float64 {
	if raw, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil { // cgroup v2: "<quota> <period>" or "max <period>"
		fields := strings.Fields(string(raw))
		if len(fields) == 2 && fields[0] != "max" {
			quota, errQuota := strconv.ParseFloat(fields[0], 64)
			period, errPeriod := strconv.ParseFloat(fields[1], 64)
			if errQuota == nil && errPeriod == nil && period > 0 {
				return quota / period
			}
		}
		return 0
	}
	quota, errQuota := readNumber("/sys/fs/cgroup/cpu/cpu.cfs_quota_us") // cgroup v1
	period, errPeriod := readNumber("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if errQuota == nil && errPeriod == nil && quota > 0 && period > 0 {
		return float64(quota) / float64(period)
	}
	return 0
}

// cgroupMemoryBytes returns the container memory limit, or 0 when there is none.
func cgroupMemoryBytes() int64 {
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		if value, err := readNumber(path); err == nil && value > 0 && value < 1<<50 {
			return value
		}
	}
	return 0
}

func readNumber(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
}
