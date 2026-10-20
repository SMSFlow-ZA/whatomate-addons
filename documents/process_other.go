//go:build !linux

package documents

import "os/exec"

func isolateProcess(cmd *exec.Cmd) {}
