package tailer

import (
	"syscall"
	"time"
)

func birthTimeFromStat(st *syscall.Stat_t) time.Time {
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
}
