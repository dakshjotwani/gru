//go:build !darwin

package tailer

import (
	"syscall"
	"time"
)

func birthTimeFromStat(_ *syscall.Stat_t) time.Time {
	return time.Time{}
}
