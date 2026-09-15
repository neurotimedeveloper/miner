//go:build !unix

package ff

import "os/exec"

// configureProcessGroup is a no-op where process groups are unavailable.
func configureProcessGroup(cmd *exec.Cmd) {}
