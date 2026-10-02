//go:build windows

package cmd

import "golang.org/x/sys/windows"

func splitCredentialProcess(command string) ([]string, error) {
	return windows.DecomposeCommandLine(command)
}
