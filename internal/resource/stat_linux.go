//go:build linux

package resource

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// clockTicksPerSec is the kernel's USER_HZ, the unit of the CPU times in
// /proc/[pid]/stat. It is 100 on effectively all Linux configurations; the CPU
// estimate is documented as approximate, so this constant is used rather than a
// cgo sysconf(_SC_CLK_TCK) call.
const clockTicksPerSec = 100

// readProc reads cumulative CPU time (utime+stime, in clock ticks) and resident
// memory (bytes) for pid from /proc. It returns an error when the process is gone
// or its stat files cannot be parsed.
func readProc(pid int) (uint64, int64, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	// The comm field (2) is parenthesized and may contain spaces; fields after it
	// are stable, so parse from the last ')'. fields[0] is state (field 3), so
	// utime (field 14) is index 11 and stime (field 15) is index 12.
	close := bytes.LastIndexByte(stat, ')')
	if close < 0 {
		return 0, 0, fmt.Errorf("resource: malformed stat for pid %d", pid)
	}
	fields := strings.Fields(string(stat[close+1:]))
	if len(fields) < 13 {
		return 0, 0, fmt.Errorf("resource: short stat for pid %d", pid)
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, 0, err
	}

	// /proc/[pid]/statm reports sizes in pages; the second field is resident.
	statm, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0, 0, err
	}
	mf := strings.Fields(string(statm))
	if len(mf) < 2 {
		return 0, 0, fmt.Errorf("resource: short statm for pid %d", pid)
	}
	residentPages, err := strconv.ParseInt(mf[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return utime + stime, residentPages * int64(os.Getpagesize()), nil
}
