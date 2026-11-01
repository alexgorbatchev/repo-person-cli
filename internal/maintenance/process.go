package maintenance

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const finalSyncBudget = 2 * time.Minute
const processGrace = time.Second

// Cancellation gives Git and hooks a chance to release locks before escalation.
// The timer remains armed even if the immediate child exits before descendants.
func runProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var escalation *time.Timer
	cmd.Cancel = func() error {
		err := signalGroup(cmd, syscall.SIGTERM)
		escalation = time.AfterFunc(processGrace, func() { _ = signalGroup(cmd, syscall.SIGKILL) }) // best-effort descendant cleanup
		return err
	}
	cmd.WaitDelay = 2 * processGrace
	err := cmd.Run()
	if escalation != nil {
		escalation.Stop()
	}
	return errors.Join(err, signalGroup(cmd, syscall.SIGKILL))
}
func signalGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
