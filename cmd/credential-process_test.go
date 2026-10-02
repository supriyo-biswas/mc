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
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestCredentialProcessHelper(t *testing.T) {
	output := os.Getenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT")
	if output == "" {
		return
	}
	if os.Getenv("MC_CREDENTIAL_PROCESS_TEST_FAIL") != "" {
		_, _ = os.Stderr.WriteString(output)
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString(output)
	os.Exit(0)
}

func credentialProcessTestCommand() []string {
	return []string{os.Args[0], "-test.run=^TestCredentialProcessHelper$"}
}

func TestCredentialProcessProvider(t *testing.T) {
	t.Setenv("MC_CREDENTIAL_PROCESS_TEST_OUTPUT", `{"Version":1,"AccessKeyId":"access","SecretAccessKey":"secret","SessionToken":"token"}`)

	provider := newCredentialProcessProvider(credentialProcessTestCommand(), credentials.SignatureV2)
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

	provider := newCredentialProcessProvider(credentialProcessTestCommand(), credentials.SignatureV4)
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
			provider := newCredentialProcessProvider(command, credentials.SignatureV4)
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
	provider := newCredentialProcessProvider(credentialProcessTestCommand(), credentials.SignatureV4)
	chain := &credentialProviderChain{providers: []credentials.Provider{provider}}

	if _, err := chain.Retrieve(); err == nil || !strings.Contains(err.Error(), "process failed") {
		t.Fatalf("expected process error, got %v", err)
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
