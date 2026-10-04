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
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/supriyo-biswas/mc/pkg/probe"
)

func TestInitializeRemoteClients(t *testing.T) {
	oldAliases := aliasToConfigMap
	oldConfig := cacheCfgV10
	oldLoadConfig := loadMcConfig
	oldS3New := S3New
	aliasToConfigMap = map[string]*aliasConfigV10{
		"first": {
			URL:               "https://first.example.com",
			CredentialProcess: []string{"first-helper"},
			API:               "S3v4",
			Path:              "auto",
		},
		"second": {
			URL:               "https://second.example.com",
			CredentialProcess: []string{"second-helper"},
			API:               "S3v4",
			Path:              "auto",
		},
	}
	cacheCfgV10 = newConfigV10()
	loadMcConfig = func() (*configV10, *probe.Error) {
		return cacheCfgV10, nil
	}
	t.Cleanup(func() {
		aliasToConfigMap = oldAliases
		cacheCfgV10 = oldConfig
		loadMcConfig = oldLoadConfig
		S3New = oldS3New
	})

	var initialized []string
	S3New = func(config *Config) (Client, *probe.Error) {
		initialized = append(initialized, config.Alias)
		return nil, nil
	}

	local := filepath.Join(t.TempDir(), "local")
	err := initializeRemoteClients(
		local,
		"-",
		"first/bucket/one",
		"first/bucket/two",
		"second/bucket/object",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(initialized, ","); got != "first,second" {
		t.Fatalf("expected unique clients in operand order, got %q", got)
	}
}

func TestInitializeRemoteClientsReturnsErrors(t *testing.T) {
	oldAliases := aliasToConfigMap
	oldS3New := S3New
	aliasToConfigMap = map[string]*aliasConfigV10{
		"remote": {
			URL:       "https://example.com",
			AccessKey: "access-key",
			SecretKey: "secret-key",
			API:       "S3v4",
			Path:      "auto",
		},
	}
	t.Cleanup(func() {
		aliasToConfigMap = oldAliases
		S3New = oldS3New
	})

	S3New = func(_ *Config) (Client, *probe.Error) {
		return nil, probe.NewError(errors.New("initialization failed"))
	}

	err := initializeRemoteClients("remote/bucket/object")
	if err == nil || !strings.Contains(err.ToGoError().Error(), "initialization failed") {
		t.Fatalf("expected initialization error, got %v", err)
	}
}
