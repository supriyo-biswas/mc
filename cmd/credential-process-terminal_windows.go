//go:build windows

package cmd

import (
	"errors"
	"os"
)

func openCredentialProcessTerminal() (*credentialProcessTerminal, error) {
	input, err := os.OpenFile("CONIN$", os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	output, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	return &credentialProcessTerminal{
		input:  input,
		output: output,
		close: func() error {
			return errors.Join(input.Close(), output.Close())
		},
	}, nil
}
