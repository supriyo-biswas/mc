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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7/pkg/credentials"
)

const credentialProcessStderrLimit = 64 << 10

type credentialProcessResponse struct {
	Version         *int   `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

type credentialProcessTerminal struct {
	input  io.Reader
	output io.Writer
	close  func() error
}

type credentialProcessCacheEntry struct {
	value         credentials.Value
	expiry        credentials.Expiry
	hasExpiration bool
}

func (e *credentialProcessCacheEntry) isExpired(now func() time.Time) bool {
	if !e.hasExpiration {
		return false
	}
	e.expiry.CurrentTime = now
	return e.expiry.IsExpired()
}

type credentialProcessManager struct {
	mu      sync.Mutex
	entries map[string]*credentialProcessCacheEntry
	run     func(context.Context, []string) ([]byte, error)
}

func newCredentialProcessManager(run func(context.Context, []string) ([]byte, error)) *credentialProcessManager {
	return &credentialProcessManager{
		entries: make(map[string]*credentialProcessCacheEntry),
		run:     run,
	}
}

var defaultCredentialProcessManager = newCredentialProcessManager(runCredentialProcess)

func credentialProcessKey(command []string) string {
	// NUL cannot occur in an operating-system argument, so it is an
	// unambiguous separator for the exact argv passed to the helper.
	return strings.Join(command, "\x00")
}

func (m *credentialProcessManager) retrieve(ctx context.Context, command []string, now func() time.Time) (credentials.Value, error) {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return credentials.Value{}, errors.New("credential process command is empty")
	}

	key := credentialProcessKey(command)
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, ok := m.entries[key]; ok && !entry.isExpired(now) {
		return entry.value, nil
	}

	output, err := m.run(ctx, command)
	if err != nil {
		return credentials.Value{}, err
	}

	var response credentialProcessResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return credentials.Value{}, fmt.Errorf("credential process %q returned invalid JSON: %w", command[0], err)
	}
	if response.Version != nil && *response.Version != 1 {
		return credentials.Value{}, fmt.Errorf("credential process %q returned unsupported version %d", command[0], *response.Version)
	}
	if response.AccessKeyID == "" || response.SecretAccessKey == "" {
		return credentials.Value{}, fmt.Errorf("credential process %q must return AccessKeyId and SecretAccessKey", command[0])
	}

	entry := &credentialProcessCacheEntry{
		value: credentials.Value{
			AccessKeyID:     response.AccessKeyID,
			SecretAccessKey: response.SecretAccessKey,
			SessionToken:    response.SessionToken,
		},
	}
	if response.Expiration != "" {
		expiration, err := time.Parse(time.RFC3339, response.Expiration)
		if err != nil {
			return credentials.Value{}, fmt.Errorf("credential process %q returned invalid Expiration: %w", command[0], err)
		}
		if !expiration.After(now()) {
			return credentials.Value{}, fmt.Errorf("credential process %q returned expired credentials", command[0])
		}
		entry.value.Expiration = expiration
		entry.hasExpiration = true
		entry.expiry.CurrentTime = now
		entry.expiry.SetExpiration(expiration, credentials.DefaultExpiryWindow)
	}

	m.entries[key] = entry
	return entry.value, nil
}

func (m *credentialProcessManager) isExpired(command []string, now func() time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.entries[credentialProcessKey(command)]
	return !ok || entry.isExpired(now)
}

type credentialProcessProvider struct {
	command    []string
	signerType credentials.SignatureType
	manager    *credentialProcessManager
	retrieved  bool
	now        func() time.Time
}

func newCredentialProcessProvider(command []string, signerType credentials.SignatureType) *credentialProcessProvider {
	return &credentialProcessProvider{
		command:    append([]string(nil), command...),
		signerType: signerType,
		manager:    defaultCredentialProcessManager,
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
	ctx := globalContext
	if ctx == nil {
		ctx = context.Background()
	}
	value, err := p.manager.retrieve(ctx, p.command, p.now)
	if err != nil {
		return credentials.Value{}, err
	}
	value.SignerType = p.signerType
	p.retrieved = true
	return value, nil
}

func (p *credentialProcessProvider) IsExpired() bool {
	return !p.retrieved || p.manager.isExpired(p.command, p.now)
}

type tailBuffer struct {
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append(b.data[:0], b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	return string(b.data)
}

type credentialProcessStderr struct {
	terminal io.Writer
	tail     *tailBuffer
}

func (w credentialProcessStderr) Write(p []byte) (int, error) {
	_, _ = w.tail.Write(p)
	if w.terminal != nil {
		_, _ = w.terminal.Write(p)
	}
	return len(p), nil
}

func runCredentialProcess(ctx context.Context, command []string) ([]byte, error) {
	return runCredentialProcessWithTerminal(ctx, command, openCredentialProcessTerminal)
}

func runCredentialProcessWithTerminal(ctx context.Context, command []string, openTerminal func() (*credentialProcessTerminal, error)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	var stdout bytes.Buffer
	stderr := &tailBuffer{limit: credentialProcessStderrLimit}
	cmd.Stdout = &stdout

	terminal, err := openTerminal()
	if err == nil {
		defer func() {
			_ = terminal.close()
		}()
		cmd.Stdin = terminal.input
		cmd.Stderr = credentialProcessStderr{terminal: terminal.output, tail: stderr}
	} else {
		cmd.Stderr = stderr
	}

	if err := cmd.Run(); err != nil {
		diagnostic := strings.TrimSpace(stderr.String())
		if diagnostic != "" {
			return nil, fmt.Errorf("credential process %q failed: %w: %s", command[0], err, diagnostic)
		}
		return nil, fmt.Errorf("credential process %q failed: %w", command[0], err)
	}
	return stdout.Bytes(), nil
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
		value, err := provider.RetrieveWithCredContext(ctx)
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
