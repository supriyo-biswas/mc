//go:build !windows

package cmd

import "github.com/google/shlex"

func splitCredentialProcess(command string) ([]string, error) {
	return shlex.Split(command)
}
