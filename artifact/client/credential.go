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
	"net"
	"net/url"
	"os"
	"path"
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

// LookupCredential returns every credential sources hold for ref, in the
// order the kubelet tries them when it pulls ref with the same Docker configs,
// for RemoteOptions.Credentials, and the sources that hold none, with why.
// With none, a Target reads anonymously. It never runs a credential helper.
//
// Sources are taken in order, as the kubelet takes a pod's pull secret
// before the node's config, and each is matched as the kubelet's keyring,
// BasicDockerKeyring in k8s.io/kubernetes pkg/credentialprovider, matches
// one. An auths key, with or without a scheme, is a host, an optional port
// and an optional path, less a /v1/ or /v2/ API prefix. It matches when it
// has the port of ref.Registry and as many dot-separated labels, each a
// path.Match pattern for the label in its place, and its path is a string
// prefix of /ref.Repository, all case-sensitive. Matching keys go in reverse
// lexical order, so a key with a path comes before its host alone, and keys
// that reduce alike in the order of their spelling. When no key matches and
// the kubelet takes ref for Docker Hub, as it also takes any registry with
// no dot or colon, the index.docker.io key docker login writes matches. An
// empty entry gives no credential, and a malformed one none from its
// source, which the kubelet cannot use either.
func LookupCredential(ref Reference, sources []CredentialSource) ([]Credential, []SkippedSource) {
	var creds []Credential
	var skipped []SkippedSource
	for _, source := range sources {
		found, reason := source.lookup(ref)
		if reason != "" {
			skipped = append(skipped, SkippedSource{Source: source.name, Reason: reason})
		}
		creds = append(creds, found...)
	}
	return creds, skipped
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

// lookup returns the credentials the source holds for ref, or why it holds
// none. No reason quotes the content: a JSON error can quote the bytes it
// stopped at, which may be a secret.
func (s CredentialSource) lookup(ref Reference) ([]Credential, string) {
	content := s.content
	if s.path != "" {
		var err error
		if content, err = os.ReadFile(s.path); errors.Is(err, fs.ErrNotExist) {
			return nil, "file does not exist"
		} else if err != nil {
			return nil, fmt.Sprintf("file is unreadable: %v", err)
		}
	}
	var config dockerConfig
	if err := json.Unmarshal(content, &config); err != nil {
		return nil, "malformed JSON"
	}

	keys := matchKeys(config.Auths, ref)
	var creds []Credential
	for _, key := range keys {
		cred, err := parseEntry(config.Auths[key])
		if err != nil {
			return nil, fmt.Sprintf("auths entry %q is malformed: %v", key, err)
		}
		if cred != auth.EmptyCredential {
			creds = append(creds, Credential{source: s.name, auth: cred})
		}
	}
	switch helper := config.helper(ref); {
	case len(creds) > 0:
		return creds, ""
	case helper != "":
		return nil, fmt.Sprintf("credential helper %q is configured and not run", helper)
	case len(keys) == 0:
		return nil, fmt.Sprintf("no auths entry for %s", ref.Name())
	}
	return nil, fmt.Sprintf("auths entry %q is empty", keys[0])
}

// helper returns the credential helper the config names for ref, if any.
func (c dockerConfig) helper(ref Reference) string {
	if keys := matchKeys(c.CredHelpers, ref); len(keys) > 0 {
		return c.CredHelpers[keys[0]]
	}
	return c.CredsStore
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
		// Padded or not, as the kubelet decodes it.
		decode := base64.RawStdEncoding.DecodeString
		if strings.HasSuffix(strings.TrimSpace(entry.Auth), "=") {
			decode = base64.StdEncoding.DecodeString
		}
		decoded, err := decode(entry.Auth)
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

// matchKeys returns the keys of m that match ref, in the order
// LookupCredential gives.
func matchKeys[V any](m map[string]V, ref Reference) []string {
	reduced := map[string][]string{}
	for _, key := range slices.Sorted(maps.Keys(m)) {
		if r, ok := reduceKey(key); ok {
			reduced[r] = append(reduced[r], key)
		}
	}
	var keys []string
	for _, r := range slices.Backward(slices.Sorted(maps.Keys(reduced))) {
		if keyMatches(r, ref.Name()) {
			keys = append(keys, reduced[r]...)
		}
	}
	if len(keys) == 0 && kubeletDockerHub(ref.Registry) {
		keys = reduced[dockerHubLegacy]
	}
	return keys
}

// reduceKey reduces an auths key to its host and path as
// BasicDockerKeyring.Add does, or reports that it ignores the key.
func reduceKey(key string) (string, bool) {
	if !strings.HasPrefix(key, "https://") && !strings.HasPrefix(key, "http://") {
		key = "https://" + key
	}
	u, err := url.Parse(key)
	if err != nil {
		return "", false
	}
	p := u.Path
	if strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/v2/") {
		p = p[len("/v1"):]
	}
	if p == "/" {
		p = ""
	}
	return u.Host + p, true
}

// keyMatches reports whether a reduced key matches image, a reference's
// Name, as URLsMatchStr of pkg/credentialprovider does.
func keyMatches(key, image string) bool {
	glob, err := url.Parse("https://" + key)
	if err != nil {
		return false
	}
	target, err := url.Parse("https://" + image)
	if err != nil {
		return false
	}
	patterns, globPort := splitHost(glob.Host)
	labels, port := splitHost(target.Host)
	if globPort != port || len(patterns) != len(labels) || !strings.HasPrefix(target.Path, glob.Path) {
		return false
	}
	for i, pattern := range patterns {
		if matched, _ := path.Match(pattern, labels[i]); !matched {
			return false
		}
	}
	return true
}

// splitHost splits a host and optional port into the host's dot-separated
// labels and the port.
func splitHost(hostport string) ([]string, string) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}
	return strings.Split(host, "."), port
}

// kubeletDockerHub reports whether the kubelet takes an image on registry
// for Docker Hub when it falls back to the index.docker.io key, as
// isDefaultRegistryMatch of pkg/credentialprovider does.
func kubeletDockerHub(registry string) bool {
	return registry == dockerHub || registry == dockerHubLegacy ||
		(registry != "" && !strings.ContainsAny(registry, ".:"))
}
