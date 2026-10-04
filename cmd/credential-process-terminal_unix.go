//go:build !windows

package cmd

import "os"

func openCredentialProcessTerminal() (*credentialProcessTerminal, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &credentialProcessTerminal{
		input:  terminal,
		output: terminal,
		close:  terminal.Close,
	}, nil
}
