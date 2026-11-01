package maintenance

import (
	"syscall"
	"time"
)

func inodeChange(stat *syscall.Stat_t) time.Time {
	return time.Unix(stat.Ctimespec.Sec, stat.Ctimespec.Nsec)
}
