// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/minio/minio-go/v7/pkg/credentials"
)

type credentialProcessResponse struct {
	Version         *int   `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

type credentialProcessProvider struct {
	credentials.Expiry
	command       []string
	signerType    credentials.SignatureType
	retrieved     bool
	hasExpiration bool
	now           func() time.Time
}

func newCredentialProcessProvider(command []string, signerType credentials.SignatureType) *credentialProcessProvider {
	return &credentialProcessProvider{
		command:    append([]string(nil), command...),
		signerType: signerType,
		now:        time.Now,
	}
}

func (p *credentialProcessProvider) Retrieve() (credentials.Value, error) {
	return p.retrieve()
}

func (p *credentialProcessProvider) RetrieveWithCredContext(_ *credentials.CredContext) (credentials.Value, error) {
	return p.retrieve()
}

func (p *credentialProcessProvider) retrieve() (credentials.Value, error) {
	if len(p.command) == 0 || strings.TrimSpace(p.command[0]) == "" {
		return credentials.Value{}, errors.New("credential process command is empty")
	}

	cmd := exec.Command(p.command[0], p.command[1:]...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return credentials.Value{}, fmt.Errorf("credential process %q failed: %w: %s", p.command[0], err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return credentials.Value{}, fmt.Errorf("credential process %q failed: %w", p.command[0], err)
	}

	var response credentialProcessResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return credentials.Value{}, fmt.Errorf("credential process %q returned invalid JSON: %w", p.command[0], err)
	}
	if response.Version != nil && *response.Version != 1 {
		return credentials.Value{}, fmt.Errorf("credential process %q returned unsupported version %d", p.command[0], *response.Version)
	}
	if response.AccessKeyID == "" || response.SecretAccessKey == "" {
		return credentials.Value{}, fmt.Errorf("credential process %q must return AccessKeyId and SecretAccessKey", p.command[0])
	}

	value := credentials.Value{
		AccessKeyID:     response.AccessKeyID,
		SecretAccessKey: response.SecretAccessKey,
		SessionToken:    response.SessionToken,
		SignerType:      p.signerType,
	}

	p.hasExpiration = response.Expiration != ""
	if p.hasExpiration {
		expiration, err := time.Parse(time.RFC3339, response.Expiration)
		if err != nil {
			return credentials.Value{}, fmt.Errorf("credential process %q returned invalid Expiration: %w", p.command[0], err)
		}
		if !expiration.After(p.now()) {
			return credentials.Value{}, fmt.Errorf("credential process %q returned expired credentials", p.command[0])
		}
		value.Expiration = expiration
		p.CurrentTime = func() time.Time { return p.now() }
		p.SetExpiration(expiration, credentials.DefaultExpiryWindow)
	}

	p.retrieved = true
	return value, nil
}

func (p *credentialProcessProvider) IsExpired() bool {
	if !p.retrieved {
		return true
	}
	if !p.hasExpiration {
		return false
	}
	return p.Expiry.IsExpired()
}

// credentialProviderChain behaves like minio-go's credentials.Chain, but it
// preserves the last provider error when no provider can return credentials.
type credentialProviderChain struct {
	providers []credentials.Provider
	current   credentials.Provider
}

func (c *credentialProviderChain) Retrieve() (credentials.Value, error) {
	return c.retrieve(nil)
}

func (c *credentialProviderChain) RetrieveWithCredContext(ctx *credentials.CredContext) (credentials.Value, error) {
	return c.retrieve(ctx)
}

func (c *credentialProviderChain) retrieve(ctx *credentials.CredContext) (credentials.Value, error) {
	var lastErr error
	for _, provider := range c.providers {
		var (
			value credentials.Value
			err   error
		)
		if ctx == nil {
			value, err = provider.Retrieve()
		} else {
			value, err = provider.RetrieveWithCredContext(ctx)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if value.AccessKeyID == "" && value.SecretAccessKey == "" {
			continue
		}
		c.current = provider
		return value, nil
	}
	if lastErr != nil {
		return credentials.Value{}, lastErr
	}
	return credentials.Value{SignerType: credentials.SignatureAnonymous}, nil
}

func (c *credentialProviderChain) IsExpired() bool {
	return c.current == nil || c.current.IsExpired()
}
