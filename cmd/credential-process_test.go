// Copyright (c) 2015-2026 MinIO, Inc. and other contributors
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
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestCredentialProcessHelper(_ *testing.T) {
	output := os.Getenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT")
	if output == "" {
		return
	}
	if os.Getenv("MC_CREDENTIAL_PROCESS_TEST_FAIL") != "" {
		_, _ = os.Stderr.WriteString(output)
		os.Exit(2)
	}
	if os.Getenv("MC_CREDENTIAL_PROCESS_TEST_READ_STDIN") != "" {
		_, _ = os.Stderr.WriteString("credential prompt")
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(3)
		}
		output = fmt.Sprintf(`{"AccessKeyId":%q,"SecretAccessKey":"secret"}`, strings.TrimSpace(string(input)))
	}
	_, _ = os.Stdout.WriteString(output)
	os.Exit(0)
}

func credentialProcessTestCommand(unique ...string) []string {
	command := []string{os.Args[0], "-test.run=^TestCredentialProcessHelper$"}
	if len(unique) > 0 {
		command = append(command, "--")
		command = append(command, unique...)
	}
	return command
}

func newTestCredentialProcessProvider(command []string, signerType credentials.SignatureType) *credentialProcessProvider {
	provider := newCredentialProcessProvider(command, signerType)
	provider.manager = newCredentialProcessManager(runCredentialProcess)
	return provider
}

func TestCredentialProcessProvider(t *testing.T) {
	t.Setenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT", `{"Version":1,"AccessKeyId":"access","SecretAccessKey":"secret","SessionToken":"token"}`)

	provider := newTestCredentialProcessProvider(credentialProcessTestCommand(), credentials.SignatureV2)
	value, err := provider.Retrieve()
	if err != nil {
		t.Fatal(err)
	}
	if value.AccessKeyID != "access" || value.SecretAccessKey != "secret" || value.SessionToken != "token" {
		t.Fatalf("unexpected credentials: %#v", value)
	}
	if value.SignerType != credentials.SignatureV2 {
		t.Fatalf("expected V2 signer, got %v", value.SignerType)
	}
	if provider.IsExpired() {
		t.Fatal("credentials without Expiration must not expire")
	}
}

func TestCredentialProcessProviderExpiration(t *testing.T) {
	now := time.Date(2026, time.October, 2, 10, 0, 0, 0, time.UTC)
	expiration := now.Add(time.Hour)
	t.Setenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT", `{"AccessKeyId":"access","SecretAccessKey":"secret","Expiration":"`+expiration.Format(time.RFC3339)+`"}`)

	provider := newTestCredentialProcessProvider(credentialProcessTestCommand(), credentials.SignatureV4)
	provider.now = func() time.Time { return now }
	value, err := provider.Retrieve()
	if err != nil {
		t.Fatal(err)
	}
	if !value.Expiration.Equal(expiration) {
		t.Fatalf("expected expiration %v, got %v", expiration, value.Expiration)
	}
	if provider.IsExpired() {
		t.Fatal("fresh credentials reported expired")
	}
	provider.now = func() time.Time { return now.Add(49 * time.Minute) }
	if !provider.IsExpired() {
		t.Fatal("provider did not apply the early expiration window")
	}
}

func TestCredentialProcessProviderErrors(t *testing.T) {
	now := time.Date(2026, time.October, 2, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		output  string
		fail    bool
		wantErr string
	}{
		{name: "empty command", wantErr: "command is empty"},
		{name: "process failure", output: "helper failed", fail: true, wantErr: "helper failed"},
		{name: "invalid JSON", output: `{`, wantErr: "invalid JSON"},
		{name: "unsupported version", output: `{"Version":2,"AccessKeyId":"access","SecretAccessKey":"secret"}`, wantErr: "unsupported version 2"},
		{name: "missing access key", output: `{"SecretAccessKey":"secret"}`, wantErr: "must return AccessKeyId and SecretAccessKey"},
		{name: "invalid expiration", output: `{"AccessKeyId":"access","SecretAccessKey":"secret","Expiration":"tomorrow"}`, wantErr: "invalid Expiration"},
		{name: "expired", output: `{"AccessKeyId":"access","SecretAccessKey":"secret","Expiration":"2026-10-02T09:59:59Z"}`, wantErr: "returned expired credentials"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := credentialProcessTestCommand()
			if test.name == "empty command" {
				command = nil
			} else {
				t.Setenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT", test.output)
				if test.fail {
					t.Setenv("MC_CREDENTIAL_PROCESS_TEST_FAIL", "1")
				}
			}
			provider := newTestCredentialProcessProvider(command, credentials.SignatureV4)
			provider.now = func() time.Time { return now }
			_, err := provider.Retrieve()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestCredentialProviderChainPreservesErrors(t *testing.T) {
	t.Setenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT", "process failed")
	t.Setenv("MC_CREDENTIAL_PROCESS_TEST_FAIL", "1")
	provider := newTestCredentialProcessProvider(credentialProcessTestCommand(), credentials.SignatureV4)
	chain := &credentialProviderChain{providers: []credentials.Provider{provider}}

	if _, err := chain.Retrieve(); err == nil || !strings.Contains(err.Error(), "process failed") {
		t.Fatalf("expected process error, got %v", err)
	}
}

func TestCredentialProcessManagerSharesCredentials(t *testing.T) {
	var calls atomic.Int32
	manager := newCredentialProcessManager(func(_ context.Context, _ []string) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"AccessKeyId":"access","SecretAccessKey":"secret"}`), nil
	})
	first := newCredentialProcessProvider([]string{"helper", "same"}, credentials.SignatureV4)
	first.manager = manager
	second := newCredentialProcessProvider([]string{"helper", "same"}, credentials.SignatureV2)
	second.manager = manager

	firstValue, err := first.Retrieve()
	if err != nil {
		t.Fatal(err)
	}
	secondValue, err := second.Retrieve()
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one helper invocation, got %d", calls.Load())
	}
	if firstValue.SignerType != credentials.SignatureV4 || secondValue.SignerType != credentials.SignatureV2 {
		t.Fatalf("shared credentials lost signer types: %v, %v", firstValue.SignerType, secondValue.SignerType)
	}
}

func TestCredentialProcessManagerSerializesHelpers(t *testing.T) {
	var active, maximum atomic.Int32
	manager := newCredentialProcessManager(func(_ context.Context, _ []string) ([]byte, error) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return []byte(`{"AccessKeyId":"access","SecretAccessKey":"secret"}`), nil
	})

	providers := []*credentialProcessProvider{
		newCredentialProcessProvider([]string{"helper", "first"}, credentials.SignatureV4),
		newCredentialProcessProvider([]string{"helper", "second"}, credentials.SignatureV4),
	}
	var wait sync.WaitGroup
	for _, provider := range providers {
		provider.manager = manager
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := provider.Retrieve(); err != nil {
				t.Errorf("retrieve credentials: %v", err)
			}
		}()
	}
	wait.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("credential helpers overlapped: %d active", maximum.Load())
	}
}

func TestCredentialProcessManagerRefreshesOnce(t *testing.T) {
	now := time.Date(2026, time.October, 2, 10, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	manager := newCredentialProcessManager(func(_ context.Context, _ []string) ([]byte, error) {
		calls.Add(1)
		return []byte(fmt.Sprintf(`{"AccessKeyId":"access","SecretAccessKey":"secret","Expiration":%q}`, now.Add(time.Hour).Format(time.RFC3339))), nil
	})
	providers := []*credentialProcessProvider{
		newCredentialProcessProvider([]string{"helper"}, credentials.SignatureV4),
		newCredentialProcessProvider([]string{"helper"}, credentials.SignatureV4),
	}
	for _, provider := range providers {
		provider.manager = manager
		provider.now = func() time.Time { return now }
		if _, err := provider.Retrieve(); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one initial invocation, got %d", calls.Load())
	}

	now = now.Add(49 * time.Minute)
	var wait sync.WaitGroup
	for _, provider := range providers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := provider.Retrieve(); err != nil {
				t.Errorf("refresh credentials: %v", err)
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 2 {
		t.Fatalf("expected one shared refresh, got %d total invocations", calls.Load())
	}
}

func TestCredentialProcessUsesTerminalInput(t *testing.T) {
	t.Setenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT", "placeholder")
	t.Setenv("MC_CREDENTIAL_PROCESS_TEST_READ_STDIN", "1")
	var terminalOutput bytes.Buffer
	output, err := runCredentialProcessWithTerminal(context.Background(), credentialProcessTestCommand(t.Name()), func() (*credentialProcessTerminal, error) {
		return &credentialProcessTerminal{
			input:  strings.NewReader("from-terminal\n"),
			output: &terminalOutput,
			close:  func() error { return nil },
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"AccessKeyId":"from-terminal"`) {
		t.Fatalf("helper did not read terminal input: %s", output)
	}
	if terminalOutput.String() != "credential prompt" {
		t.Fatalf("helper stderr was not routed to the terminal: %q", terminalOutput.String())
	}
}

func TestCredentialProcessFailureIsNotCached(t *testing.T) {
	var calls atomic.Int32
	manager := newCredentialProcessManager(func(_ context.Context, _ []string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("temporary failure")
		}
		return []byte(`{"AccessKeyId":"access","SecretAccessKey":"secret"}`), nil
	})
	provider := newCredentialProcessProvider([]string{"helper"}, credentials.SignatureV4)
	provider.manager = manager
	if _, err := provider.Retrieve(); err == nil {
		t.Fatal("expected first retrieval to fail")
	}
	if _, err := provider.Retrieve(); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
}

func TestS3FactoryPreflightsCredentialProcess(t *testing.T) {
	t.Setenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT", "not-json")
	factory := newFactory()
	_, err := factory(&Config{
		HostURL:           "https://example.com",
		CredentialProcess: credentialProcessTestCommand(t.Name()),
		Transport:         http.DefaultTransport,
	})
	if err == nil || !strings.Contains(err.ToGoError().Error(), "returned invalid JSON") {
		t.Fatalf("expected credential process to fail during client construction, got %v", err)
	}
}

func TestCredentialProcessConfigJSON(t *testing.T) {
	config := aliasConfigV10{CredentialProcess: []string{"helper", "--profile", "test"}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"credentialProcess":["helper","--profile","test"]`) {
		t.Fatalf("credentialProcess missing from config: %s", data)
	}

	var roundTrip aliasConfigV10
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if strings.Join(roundTrip.CredentialProcess, "|") != "helper|--profile|test" {
		t.Fatalf("unexpected credential process: %#v", roundTrip.CredentialProcess)
	}
}

func TestCredentialProcessChangesConfigHash(t *testing.T) {
	first := &Config{HostURL: "https://example.com", CredentialProcess: []string{"helper", "first"}}
	second := &Config{HostURL: "https://example.com", CredentialProcess: []string{"helper", "second"}}
	if getConfigHash(first) == getConfigHash(second) {
		t.Fatal("different credential processes produced the same config hash")
	}
}
