package maintenance

import (
	"syscall"
	"time"
)

func inodeChange(stat *syscall.Stat_t) time.Time {
	return time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)
}
