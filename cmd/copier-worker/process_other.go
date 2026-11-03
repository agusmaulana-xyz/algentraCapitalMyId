//go:build !windows

package main

import (
	"os"
	"os/exec"
	"time"
)

func prepareChild(cmd *exec.Cmd)                                                    {}
func manageMT5Charts(processID uint32, stopped <-chan struct{}, enforceSingle bool) {}
func requestMT5Close(processID uint32)                                              {}

func stopChildProcess(cmd *exec.Cmd, done <-chan error, timeout time.Duration) error {
	select {
	case err := <-done:
		return err
	default:
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		_ = cmd.Process.Kill()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return os.ErrDeadlineExceeded
		}
	}
}
