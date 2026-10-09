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

var _ = Describe("LookupCredential", func() {
	const registry = "ghcr.io"

	It("takes the first source with an entry and reports the ones skipped before it", func() {
		cred, skipped := LookupCredential(registry, []CredentialSource{
			configSource("other", onlyEntry("quay.io", `{"username":"placeholder-user","password":"placeholder-password"}`)),
			configSource("first", onlyEntry(registry, `{"username":"placeholder-first","password":"placeholder-password"}`)),
			configSource("second", onlyEntry(registry, `{"username":"placeholder-second","password":"placeholder-password"}`)),
		})
		Expect(cred.Source()).To(Equal("first"))
		Expect(cred.auth.Username).To(Equal("placeholder-first"))
		Expect(skipped).To(Equal([]SkippedSource{{Source: "other", Reason: "no auths entry for ghcr.io"}}))
	})

	DescribeTable("skips a source with a reason",
		func(source func() CredentialSource, reason string) {
			src := source()
			cred, skipped := LookupCredential(registry, []CredentialSource{src})
			Expect(cred).To(Equal(Credential{}))
			Expect(skipped).To(HaveLen(1))
			Expect(skipped[0].Source).To(Equal(src.name))
			Expect(skipped[0].Reason).To(ContainSubstring(reason))
		},
		Entry("with no entry for the registry", func() CredentialSource {
			return configSource("s", onlyEntry("quay.io", `{"auth":"`+basicAuth("placeholder-user:placeholder-password")+`"}`))
		}, "no auths entry for ghcr.io"),
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
			return configSource("s", onlyEntry(registry, `"placeholder"`))
		}, `auths entry "ghcr.io" is malformed: not an object of string fields`),
		Entry("whose auth is not base64", func() CredentialSource {
			return configSource("s", onlyEntry(registry, `{"auth":"placeholder!"}`))
		}, "auth is not base64"),
		Entry("whose auth has no colon", func() CredentialSource {
			return configSource("s", onlyEntry(registry, `{"auth":"`+basicAuth("placeholder")+`"}`))
		}, "auth is not user:password"),
		Entry("whose entry is empty", func() CredentialSource {
			return configSource("s", onlyEntry(registry, `{}`))
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
		cred, skipped := LookupCredential(registry, nil)
		Expect(cred).To(Equal(Credential{}))
		Expect(cred.Source()).To(BeEmpty())
		Expect(skipped).To(BeEmpty())

		cred, skipped = LookupCredential(registry, []CredentialSource{configSource("s", `{}`)})
		Expect(cred).To(Equal(Credential{}))
		Expect(skipped).To(HaveLen(1))
	})

	DescribeTable("matches a key on its host",
		func(registry, key string) {
			cred, skipped := LookupCredential(registry, []CredentialSource{
				configSource("s", onlyEntry(key, `{"username":"placeholder-user","password":"placeholder-password"}`)),
			})
			Expect(skipped).To(BeEmpty())
			Expect(cred.auth.Username).To(Equal("placeholder-user"))
		},
		Entry("docker.io as docker.io", "docker.io", "docker.io"),
		Entry("docker.io as index.docker.io", "docker.io", "index.docker.io"),
		Entry("docker.io as the docker login key", "docker.io", "https://index.docker.io/v1/"),
		Entry("docker.io as registry-1.docker.io", "docker.io", "registry-1.docker.io"),
		Entry("docker.io in upper case", "docker.io", "HTTPS://Index.Docker.IO/v1/"),
		Entry("a host behind a scheme and path", "ghcr.io", "https://ghcr.io/v2/"),
		Entry("a host in upper case", "ghcr.io", "GHCR.IO"),
		Entry("a host and port", "localhost:5000", "http://localhost:5000"),
	)

	It("prefers the exact key, then the first matching key in sorted order", func() {
		cred, _ := LookupCredential(registry, []CredentialSource{configSource("s", `{"auths":{
			"http://ghcr.io": {"username":"placeholder-normalised","password":"placeholder-password"},
			"ghcr.io": {"username":"placeholder-exact","password":"placeholder-password"}}}`)})
		Expect(cred.auth.Username).To(Equal("placeholder-exact"))

		cred, _ = LookupCredential(registry, []CredentialSource{configSource("s", `{"auths":{
			"https://ghcr.io": {"username":"placeholder-second","password":"placeholder-password"},
			"http://ghcr.io": {"username":"placeholder-first","password":"placeholder-password"}}}`)})
		Expect(cred.auth.Username).To(Equal("placeholder-first"))
	})

	It("takes auth over username and password, splitting it at the first colon", func() {
		cred, _ := LookupCredential(registry, []CredentialSource{configSource("s", onlyEntry(registry,
			`{"auth":"`+basicAuth("placeholder-user:placeholder:password")+`","username":"placeholder-other","password":"placeholder-other"}`))})
		Expect(cred.auth).To(Equal(auth.Credential{Username: "placeholder-user", Password: "placeholder:password"}))
	})

	It("maps identitytoken to the refresh token and registrytoken to the access token", func() {
		cred, _ := LookupCredential(registry, []CredentialSource{configSource("s", onlyEntry(registry,
			`{"identitytoken":"placeholder-identity","registrytoken":"placeholder-registry"}`))})
		Expect(cred.auth).To(Equal(auth.Credential{RefreshToken: "placeholder-identity", AccessToken: "placeholder-registry"}))
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
		cred, skipped := LookupCredential(ref.Registry, []CredentialSource{
			configSource("truncated", `{"auths":{"`+ref.Registry+`":{"password":"placeholder-secret-1"`),
			configSource("unquoted", onlyEntry(ref.Registry, `{"auth":placeholder-secret-2}`)),
			configSource("numeric", onlyEntry(ref.Registry, `{"username":"placeholder-user","password":7357735773577357}`)),
			configSource("not base64", onlyEntry(ref.Registry, `{"auth":"placeholder-secret-3!"}`)),
			configSource("no colon", onlyEntry(ref.Registry, `{"auth":"`+basicAuth("placeholder-secret-4")+`"}`)),
			configSource("string entry", onlyEntry(ref.Registry, `"placeholder-secret-5"`)),
			configSource("winner", onlyEntry(ref.Registry, `{"auth":"`+basicAuth("placeholder-user:placeholder-secret-6")+
				`","identitytoken":"placeholder-secret-7","registrytoken":"placeholder-secret-8"}`)),
		})
		Expect(cred.Source()).To(Equal("winner"))
		Expect(skipped).To(HaveLen(6))

		target, err := NewRemote(ref, RemoteOptions{Credential: cred, HTTPClient: server.Client()})
		Expect(err).NotTo(HaveOccurred())
		_, err = target.resolve(context.Background())
		Expect(err).To(MatchError(ErrUnauthorized))

		shown := []string{err.Error(), fmt.Sprint(RemoteOptions{Credential: cred})}
		for _, s := range skipped {
			shown = append(shown, s.Reason)
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			Expect(fmt.Sprintf(format, cred)).To(ContainSubstring("winner"))
			shown = append(shown, fmt.Sprintf(format, cred), fmt.Sprintf(format, &cred))
		}
		for _, text := range shown {
			for _, secret := range secrets {
				Expect(text).NotTo(ContainSubstring(secret))
			}
		}
	})
})
