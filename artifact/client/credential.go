// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	"oras.land/oras-go/v2/registry/remote/auth"
)

// CredentialSource is a Docker config.json to look a registry credential up
// in.
type CredentialSource struct {
	name    string
	path    string
	content []byte
}

// DockerConfigJSON is a source holding content, the .dockerconfigjson of a
// kubernetes.io/dockerconfigjson Secret. name labels it in skip reasons and
// as Credential.Source.
func DockerConfigJSON(name string, content []byte) CredentialSource {
	return CredentialSource{name: name, content: content}
}

// DockerConfigFile is a source read from path when a credential is looked
// up. The path labels it.
func DockerConfigFile(path string) CredentialSource {
	return CredentialSource{name: path, path: path}
}

// Credential authenticates to one registry. The zero Credential is
// anonymous. It formats as the name of its source alone, so printing one
// never shows a secret.
type Credential struct {
	source string
	auth   auth.Credential
}

// Source names the source the credential came from, or is empty when it is
// anonymous.
func (c Credential) Source() string { return c.source }

func (c Credential) String() string { return c.source }

func (c Credential) GoString() string { return c.source }

// SkippedSource is a source LookupCredential passed over, and why.
type SkippedSource struct {
	Source string
	Reason string
}

// LookupCredential returns the credential for registry, a Reference.Registry,
// from the first source with an auths entry for it, and the sources skipped
// before that one. It never runs a credential helper. With no entry anywhere
// the credential is the zero, anonymous one.
func LookupCredential(registry string, sources []CredentialSource) (Credential, []SkippedSource) {
	var skipped []SkippedSource
	for _, source := range sources {
		cred, reason := source.lookup(registry)
		if reason == "" {
			return cred, skipped
		}
		skipped = append(skipped, SkippedSource{Source: source.name, Reason: reason})
	}
	return Credential{}, skipped
}

type dockerConfig struct {
	Auths       map[string]json.RawMessage `json:"auths"`
	CredHelpers map[string]string          `json:"credHelpers"`
	CredsStore  string                     `json:"credsStore"`
}

type authEntry struct {
	Auth          string `json:"auth"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

// lookup returns the credential the source holds for registry, or why it
// holds none. No reason quotes the content: a JSON error can quote the bytes
// it stopped at, which may be a secret.
func (s CredentialSource) lookup(registry string) (Credential, string) {
	content := s.content
	if s.path != "" {
		var err error
		if content, err = os.ReadFile(s.path); errors.Is(err, fs.ErrNotExist) {
			return Credential{}, "file does not exist"
		} else if err != nil {
			return Credential{}, fmt.Sprintf("file is unreadable: %v", err)
		}
	}
	var config dockerConfig
	if err := json.Unmarshal(content, &config); err != nil {
		return Credential{}, "malformed JSON"
	}

	key, found := matchKey(config.Auths, registry)
	if !found {
		if helper := config.helper(registry); helper != "" {
			return Credential{}, helperReason(helper)
		}
		return Credential{}, fmt.Sprintf("no auths entry for %s", registry)
	}
	cred, err := parseEntry(config.Auths[key])
	if err != nil {
		return Credential{}, fmt.Sprintf("auths entry %q is malformed: %v", key, err)
	}
	if cred == auth.EmptyCredential {
		if helper := config.helper(registry); helper != "" {
			return Credential{}, helperReason(helper)
		}
		return Credential{}, fmt.Sprintf("auths entry %q is empty", key)
	}
	return Credential{source: s.name, auth: cred}, ""
}

// helper returns the credential helper the config names for registry, if any.
func (c dockerConfig) helper(registry string) string {
	if key, found := matchKey(c.CredHelpers, registry); found {
		return c.CredHelpers[key]
	}
	return c.CredsStore
}

func helperReason(helper string) string {
	return fmt.Sprintf("credential helper %q is configured and not run", helper)
}

// parseEntry decodes one auths entry. Its errors never quote the entry.
func parseEntry(raw json.RawMessage) (auth.Credential, error) {
	var entry authEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return auth.Credential{}, errors.New("not an object of string fields")
	}
	cred := auth.Credential{
		Username:     entry.Username,
		Password:     entry.Password,
		RefreshToken: entry.IdentityToken,
		AccessToken:  entry.RegistryToken,
	}
	if entry.Auth != "" {
		decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
		if err != nil {
			return auth.Credential{}, errors.New("auth is not base64")
		}
		var ok bool
		if cred.Username, cred.Password, ok = strings.Cut(string(decoded), ":"); !ok {
			return auth.Credential{}, errors.New("auth is not user:password")
		}
	}
	return cred, nil
}

// matchKey returns the key of m that names registry: the exact key if there
// is one, else the first in sorted order that names the same host.
func matchKey[V any](m map[string]V, registry string) (string, bool) {
	if _, found := m[registry]; found {
		return registry, true
	}
	host := normaliseHost(registry)
	for _, key := range slices.Sorted(maps.Keys(m)) {
		if normaliseHost(key) == host {
			return key, true
		}
	}
	return "", false
}

// normaliseHost reduces a config key or registry to its lower-case host,
// folding the Docker Hub aliases into docker.io.
func normaliseHost(s string) string {
	s = strings.ToLower(s)
	if _, rest, found := strings.Cut(s, "://"); found {
		s = rest
	}
	s, _, _ = strings.Cut(s, "/")
	if s == dockerHubLegacy || s == dockerHubAPIHost {
		return dockerHub
	}
	return s
}
