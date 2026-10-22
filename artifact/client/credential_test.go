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
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"oras.land/oras-go/v2/registry/remote/auth"
)

func configSource(name, config string) CredentialSource {
	return DockerConfigJSON(name, []byte(config))
}

func basicAuth(userPassword string) string {
	return base64.StdEncoding.EncodeToString([]byte(userPassword))
}

func onlyEntry(registry, entry string) string {
	return fmt.Sprintf(`{"auths":{%q:%s}}`, registry, entry)
}

func userEntry(username string) string {
	return fmt.Sprintf(`{"username":%q,"password":"placeholder-password"}`, username)
}

// credentialRef is the reference of image at tag 1.0.
func credentialRef(image string) Reference {
	GinkgoHelper()
	ref, err := NewReference(image, "1.0", "")
	Expect(err).NotTo(HaveOccurred())
	return ref
}

func usernames(creds []Credential) []string {
	var names []string
	for _, cred := range creds {
		names = append(names, cred.auth.Username)
	}
	return names
}

var _ = Describe("LookupCredential", func() {
	var ref Reference

	BeforeEach(func() {
		ref = credentialRef("ghcr.io/nvidia/app")
	})

	It("returns the credentials of every source in order and reports the sources that hold none", func() {
		creds, skipped := LookupCredential(ref, []CredentialSource{
			configSource("other", onlyEntry("quay.io", userEntry("placeholder-user"))),
			configSource("first", onlyEntry("ghcr.io", userEntry("placeholder-first"))),
			configSource("second", onlyEntry("ghcr.io", userEntry("placeholder-second"))),
		})
		Expect(usernames(creds)).To(Equal([]string{"placeholder-first", "placeholder-second"}))
		Expect(creds[0].Source()).To(Equal("first"))
		Expect(creds[1].Source()).To(Equal("second"))
		Expect(skipped).To(Equal([]SkippedSource{{Source: "other", Reason: "no auths entry for ghcr.io/nvidia/app"}}))
	})

	DescribeTable("skips a source with a reason",
		func(source func() CredentialSource, reason string) {
			src := source()
			creds, skipped := LookupCredential(ref, []CredentialSource{src})
			Expect(creds).To(BeEmpty())
			Expect(skipped).To(HaveLen(1))
			Expect(skipped[0].Source).To(Equal(src.name))
			Expect(skipped[0].Reason).To(ContainSubstring(reason))
		},
		Entry("with no entry for the reference", func() CredentialSource {
			return configSource("s", onlyEntry("quay.io", `{"auth":"`+basicAuth("placeholder-user:placeholder-password")+`"}`))
		}, "no auths entry for ghcr.io/nvidia/app"),
		Entry("whose file is missing", func() CredentialSource {
			return DockerConfigFile(filepath.Join(GinkgoT().TempDir(), "config.json"))
		}, "file does not exist"),
		Entry("whose file is unreadable", func() CredentialSource {
			return DockerConfigFile(GinkgoT().TempDir())
		}, "file is unreadable"),
		Entry("of malformed JSON", func() CredentialSource {
			return configSource("s", `{"auths":`)
		}, "malformed JSON"),
		Entry("whose entry is not an object", func() CredentialSource {
			return configSource("s", onlyEntry("ghcr.io", `"placeholder"`))
		}, `auths entry "ghcr.io" is malformed: not an object of string fields`),
		Entry("whose auth is not base64", func() CredentialSource {
			return configSource("s", onlyEntry("ghcr.io", `{"auth":"placeholder!"}`))
		}, "auth is not base64"),
		Entry("whose auth has no colon", func() CredentialSource {
			return configSource("s", onlyEntry("ghcr.io", `{"auth":"`+basicAuth("placeholder")+`"}`))
		}, "auth is not user:password"),
		Entry("with one matching entry malformed beside a valid one", func() CredentialSource {
			return configSource("s", `{"auths":{"ghcr.io":`+userEntry("placeholder-user")+`,"ghcr.io/nvidia":{"auth":"placeholder!"}}}`)
		}, `auths entry "ghcr.io/nvidia" is malformed: auth is not base64`),
		Entry("whose entry is empty", func() CredentialSource {
			return configSource("s", onlyEntry("ghcr.io", `{}`))
		}, `auths entry "ghcr.io" is empty`),
		Entry("that names the registry under credHelpers", func() CredentialSource {
			return configSource("s", `{"credHelpers":{"ghcr.io":"placeholder-helper"}}`)
		}, `credential helper "placeholder-helper" is configured and not run`),
		Entry("that names a credsStore", func() CredentialSource {
			return configSource("s", `{"credsStore":"placeholder-store"}`)
		}, `credential helper "placeholder-store" is configured and not run`),
		Entry("whose entry is the empty one docker login writes beside a credsStore", func() CredentialSource {
			return configSource("s", `{"auths":{"ghcr.io":{}},"credsStore":"placeholder-store"}`)
		}, `credential helper "placeholder-store" is configured and not run`),
	)

	It("is anonymous when no source has an entry", func() {
		creds, skipped := LookupCredential(ref, nil)
		Expect(creds).To(BeEmpty())
		Expect(skipped).To(BeEmpty())

		creds, skipped = LookupCredential(ref, []CredentialSource{configSource("s", `{}`)})
		Expect(creds).To(BeEmpty())
		Expect(skipped).To(HaveLen(1))
	})

	DescribeTable("matches an auths key as the kubelet's keyring does",
		func(image, key string, matches bool) {
			creds, skipped := LookupCredential(credentialRef(image), []CredentialSource{
				configSource("s", onlyEntry(key, userEntry("placeholder-user"))),
			})
			if matches {
				Expect(skipped).To(BeEmpty())
				Expect(usernames(creds)).To(Equal([]string{"placeholder-user"}))
				return
			}
			Expect(creds).To(BeEmpty())
			Expect(skipped).To(HaveLen(1))
			Expect(skipped[0].Reason).To(HavePrefix("no auths entry for "))
		},
		Entry("a host", "ghcr.io/nvidia/app", "ghcr.io", true),
		Entry("a host behind a scheme and the /v2/ prefix", "ghcr.io/nvidia/app", "https://ghcr.io/v2/", true),
		Entry("a host and port behind http://", "localhost:5000/app", "http://localhost:5000", true),
		Entry("a repository path", "ghcr.io/nvidia/app", "ghcr.io/nvidia", true),
		Entry("a repository path behind the /v1/ prefix", "ghcr.io/nvidia/app", "https://ghcr.io/v1/nvidia", true),
		Entry("a path that is only a string prefix of the repository", "ghcr.io/nvidia-labs/app", "ghcr.io/nvidia", true),
		Entry("a wildcard label", "registry.example.com/app", "*.example.com", true),
		Entry("docker.io for Docker Hub", "alpine", "docker.io", true),
		Entry("the Docker Hub key docker login writes", "alpine", "https://index.docker.io/v1/", true),
		Entry("index.docker.io for Docker Hub", "nvidia/app", "index.docker.io", true),
		Entry("the Docker Hub key for a registry with no dot or colon", "localhost/app", "index.docker.io", true),

		Entry("another repository path", "ghcr.io/other/app", "ghcr.io/nvidia", false),
		Entry("a wildcard over another number of labels", "example.com/app", "*.example.com", false),
		Entry("another port", "localhost:5001/app", "localhost:5000", false),
		Entry("a port the reference does not name", "ghcr.io/nvidia/app", "ghcr.io:443", false),
		Entry("a host in another case", "ghcr.io/nvidia/app", "GHCR.IO", false),
		Entry("registry-1.docker.io for Docker Hub", "alpine", "registry-1.docker.io", false),
		Entry("the Docker Hub key for another registry", "ghcr.io/nvidia/app", "index.docker.io", false),
		Entry("a key that does not parse as a URL", "ghcr.io/nvidia/app", "ghcr.io:port", false),
	)

	DescribeTable("MatchImage matches a matchImages entry as the kubelet's credential provider does",
		func(image, pattern string, matches bool) {
			Expect(MatchImage(pattern, credentialRef(image))).To(Equal(matches))
		},
		Entry("a wildcard registry", "myregistry.azurecr.io/app", "*.azurecr.io", true),
		Entry("wildcards in several labels", "123456789012.dkr.ecr.us-east-1.amazonaws.com/app", "*.dkr.ecr.*.amazonaws.com", true),
		Entry("a host and port", "registry.example.com:5000/app", "registry.example.com:5000", true),
		Entry("a repository path", "ghcr.io/nvidia/app", "ghcr.io/nvidia", true),
		Entry("docker.io for Docker Hub", "alpine", "docker.io", true),

		Entry("a wildcard over another number of labels", "123456789012.dkr.ecr.us-east-1.amazonaws.com/app", "*.amazonaws.com", false),
		Entry("another port", "registry.example.com:5001/app", "registry.example.com:5000", false),
		Entry("another repository path", "ghcr.io/other/app", "ghcr.io/nvidia", false),
		Entry("index.docker.io for Docker Hub, which only a docker config falls back to", "alpine", "index.docker.io", false),
	)

	It("orders the credentials of a source as the kubelet's keyring does", func() {
		creds, skipped := LookupCredential(ref, []CredentialSource{configSource("s", `{"auths":{
			"*.io": `+userEntry("placeholder-wildcard")+`,
			"https://ghcr.io": `+userEntry("placeholder-scheme")+`,
			"ghcr.io": `+userEntry("placeholder-host")+`,
			"ghcr.io/nvidia": `+userEntry("placeholder-path")+`,
			"quay.io": `+userEntry("placeholder-other")+`,
			"ghcr.io/nvidia/": {}}}`)})
		Expect(skipped).To(BeEmpty())
		Expect(usernames(creds)).To(Equal([]string{"placeholder-path", "placeholder-host", "placeholder-scheme", "placeholder-wildcard"}))
	})

	It("takes the Docker Hub key docker login writes only when no other key matches", func() {
		hub := credentialRef("alpine")
		legacy := `"https://index.docker.io/v1/": ` + userEntry("placeholder-legacy")
		creds, _ := LookupCredential(hub, []CredentialSource{configSource("s", `{"auths":{`+legacy+`}}`)})
		Expect(usernames(creds)).To(Equal([]string{"placeholder-legacy"}))

		creds, _ = LookupCredential(hub, []CredentialSource{configSource("s", `{"auths":{`+legacy+`,"docker.io/library":`+userEntry("placeholder-path")+`}}`)})
		Expect(usernames(creds)).To(Equal([]string{"placeholder-path"}))
	})

	DescribeTable("decodes auth with or without padding, as the kubelet does",
		func(encode func(string) string) {
			creds, _ := LookupCredential(ref, []CredentialSource{configSource("s", onlyEntry("ghcr.io", `{"auth":"`+encode("placeholder-user:placeholder-pw")+`"}`))})
			Expect(creds).To(HaveLen(1))
			Expect(creds[0].auth).To(Equal(auth.Credential{Username: "placeholder-user", Password: "placeholder-pw"}))
		},
		Entry("padded", basicAuth),
		Entry("unpadded", func(s string) string {
			encoded := basicAuth(s)
			Expect(encoded).To(HaveSuffix("="))
			return strings.TrimRight(encoded, "=")
		}),
	)

	It("takes auth over username and password, splitting it at the first colon", func() {
		creds, _ := LookupCredential(ref, []CredentialSource{configSource("s", onlyEntry("ghcr.io",
			`{"auth":"`+basicAuth("placeholder-user:placeholder:password")+`","username":"placeholder-other","password":"placeholder-other"}`))})
		Expect(creds).To(HaveLen(1))
		Expect(creds[0].auth).To(Equal(auth.Credential{Username: "placeholder-user", Password: "placeholder:password"}))
	})

	It("maps identitytoken to the refresh token and registrytoken to the access token", func() {
		creds, _ := LookupCredential(ref, []CredentialSource{configSource("s", onlyEntry("ghcr.io",
			`{"identitytoken":"placeholder-identity","registrytoken":"placeholder-registry"}`))})
		Expect(creds).To(HaveLen(1))
		Expect(creds[0].auth).To(Equal(auth.Credential{RefreshToken: "placeholder-identity", AccessToken: "placeholder-registry"}))
	})

	It("never shows a secret in a reason, an error or a formatted credential", func() {
		secrets := []string{
			"placeholder-secret-1", "placeholder-secret-2", "7357735773577357", "placeholder-secret-3",
			"placeholder-secret-4", "placeholder-secret-5", "placeholder-secret-6", "placeholder-secret-7",
			"placeholder-secret-8", basicAuth("placeholder-user:placeholder-secret-6"),
		}
		server, ref := newRegistry(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		creds, skipped := LookupCredential(ref, []CredentialSource{
			configSource("truncated", `{"auths":{"`+ref.Registry+`":{"password":"placeholder-secret-1"`),
			configSource("unquoted", onlyEntry(ref.Registry, `{"auth":placeholder-secret-2}`)),
			configSource("numeric", onlyEntry(ref.Registry, `{"username":"placeholder-user","password":7357735773577357}`)),
			configSource("not base64", onlyEntry(ref.Registry, `{"auth":"placeholder-secret-3!"}`)),
			configSource("no colon", onlyEntry(ref.Registry, `{"auth":"`+basicAuth("placeholder-secret-4")+`"}`)),
			configSource("string entry", onlyEntry(ref.Registry, `"placeholder-secret-5"`)),
			configSource("winner", onlyEntry(ref.Registry, `{"auth":"`+basicAuth("placeholder-user:placeholder-secret-6")+
				`","identitytoken":"placeholder-secret-7","registrytoken":"placeholder-secret-8"}`)),
		})
		Expect(creds).To(HaveLen(1))
		cred := creds[0]
		Expect(cred.Source()).To(Equal("winner"))
		Expect(skipped).To(HaveLen(6))

		target, err := NewRemote(ref, RemoteOptions{Credentials: creds, HTTPClient: server.Client()})
		Expect(err).NotTo(HaveOccurred())
		_, _, err = target.resolve(context.Background())
		Expect(err).To(MatchError(ErrUnauthorized))

		shown := []string{err.Error(), fmt.Sprint(RemoteOptions{Credentials: creds})}
		for _, s := range skipped {
			shown = append(shown, s.Reason)
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			Expect(fmt.Sprintf(format, cred)).To(ContainSubstring("winner"))
			shown = append(shown, fmt.Sprintf(format, cred), fmt.Sprintf(format, &cred), fmt.Sprintf(format, creds))
		}
		for _, text := range shown {
			for _, secret := range secrets {
				Expect(text).NotTo(ContainSubstring(secret))
			}
		}
	})
})
