// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"sync"

	"github.com/supriyo-biswas/mc/pkg/probe"
	"github.com/supriyo-biswas/mc/pkg/quick"
)

var (
	// set once during first load.
	cacheCfgV10 *configV10
	// All access to mc config file should be synchronized.
	cfgMutex = &sync.RWMutex{}
)

// aliasConfig configuration of an alias.
type aliasConfigV10 struct {
	URL               string   `json:"url"`
	AccessKey         string   `json:"accessKey,omitempty"`
	SecretKey         string   `json:"secretKey,omitempty"`
	SessionToken      string   `json:"sessionToken,omitempty"`
	CredentialProcess []string `json:"credentialProcess,omitempty"`
	API               string   `json:"api"`
	Path              string   `json:"path"`
	License           string   `json:"license,omitempty"`
	APIKey            string   `json:"apiKey,omitempty"`
	Src               string   `json:"src,omitempty"`
}

// configV10 config version.
type configV10 struct {
	Version string                    `json:"version"`
	Aliases map[string]aliasConfigV10 `json:"aliases"`
}

// newConfigV10 - new config version.
func newConfigV10() *configV10 {
	cfg := new(configV10)
	cfg.Version = globalMCConfigVersion
	cfg.Aliases = make(map[string]aliasConfigV10)
	return cfg
}

// loadConfigV10 - loads a new config.
func loadConfigV10() (*configV10, *probe.Error) {
	cfgMutex.RLock()
	defer cfgMutex.RUnlock()

	// If already cached, return the cached value.
	if cacheCfgV10 != nil {
		return cacheCfgV10, nil
	}

	if !isMcConfigExists() {
		return nil, errInvalidArgument().Trace()
	}

	// Initialize a new config loader.
	qc, e := quick.NewConfig(newConfigV10())
	if e != nil {
		return nil, probe.NewError(e)
	}

	// Load config at configPath, fails if config is not
	// accessible, malformed or version missing.
	if e = qc.Load(mustGetMcConfigPath()); e != nil {
		return nil, probe.NewError(e)
	}

	cfgV10 := qc.Data().(*configV10)

	// Cache config.
	cacheCfgV10 = cfgV10

	// Success.
	return cfgV10, nil
}

// saveConfigV10 - saves an updated config.
func saveConfigV10(cfgV10 *configV10) *probe.Error {
	cfgMutex.Lock()
	defer cfgMutex.Unlock()

	qs, e := quick.NewConfig(cfgV10)
	if e != nil {
		return probe.NewError(e)
	}

	// update the cache.
	cacheCfgV10 = cfgV10

	e = qs.Save(mustGetMcConfigPath())
	if e != nil {
		return probe.NewError(e).Trace(mustGetMcConfigPath())
	}
	return nil
}
