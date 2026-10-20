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

package preflight

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"oras.land/oras-go/v2/registry/remote/errcode"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
)

const (
	classifyRepo = "nodewright/hello"
	// Placeholder credentials; they guard nothing but the test registry.
	classifyUser     = "placeholder-user"
	classifyPassword = "placeholder-password"
)

var _ = Describe("Classify", func() {
	var (
		ctx context.Context
		reg *registrytest.Registry
	)

	payloadDir := func() string {
		GinkgoHelper()
		dir := GinkgoT().TempDir()
		config := `{"schema_version":"v1","package_name":"hello","package_version":"1.0.0","modes":{}}`
		Expect(os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600)).To(Succeed())
		return dir
	}

	pushPackage := func(spec registrytest.PackageSpec) string {
		GinkgoHelper()
		spec.Payload = []registrytest.Layer{{Dir: payloadDir()}}
		desc, _, err := registrytest.PushPackage(ctx, reg.Repository(classifyRepo), "1.0", spec)
		Expect(err).NotTo(HaveOccurred())
		return reg.Host() + "/" + classifyRepo + "@" + desc.Digest.String()
	}

	inspect := func(version string, sources ...client.CredentialSource) (string, *client.Inspection, error) {
		GinkgoHelper()
		ref, err := client.NewReference(reg.Host()+"/"+classifyRepo, version, "")
		Expect(err).NotTo(HaveOccurred())
		creds, _ := client.LookupCredential(ref, sources)
		target, err := client.NewRemote(ref, client.RemoteOptions{Credentials: creds, HTTPClient: reg.Client()})
		Expect(err).NotTo(HaveOccurred())
		inspection, err := client.Inspect(ctx, target)
		return ref.String(), inspection, err
	}

	BeforeEach(func() {
		ctx = context.Background()
		reg = registrytest.New(registrytest.Options{})
		DeferCleanup(reg.Close)
	})

	It("accepts an artifact with its pinned digest and uninstall declaration", func() {
		pinned := pushPackage(registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
		reference, inspection, err := inspect("1.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(NewResult(reference, inspection, err)).To(Equal(Result{
			Reference: reference,
			Outcome:   Outcome{Kind: KindArtifact, Pinned: pinned, Uninstall: artifact.UninstallUnsupported},
		}))
	})

	It("finds a container image with its pinned digest", func() {
		desc, err := registrytest.PushImage(ctx, reg.Repository(classifyRepo), "1.0", payloadDir())
		Expect(err).NotTo(HaveOccurred())
		Expect(NewResult(inspect("1.0")).Outcome).To(Equal(Outcome{Kind: KindImage, Pinned: reg.Host() + "/" + classifyRepo + "@" + desc.Digest.String()}))
	})

	It("rejects an artifact that declares host-files layers", func() {
		pushPackage(registrytest.PackageSpec{
			Uninstall: artifact.UninstallSupported,
			HostFiles: []registrytest.HostFilesLayer{{Layer: registrytest.Layer{Dir: payloadDir()}, Destination: "/etc/hello"}},
		})
		Expect(NewResult(inspect("1.0")).Outcome).To(Equal(Outcome{Kind: KindRejected, Reason: ReasonHostFiles}))
	})

	It("rejects an artifact that declares an interrupt", func() {
		step := artifact.InterruptStep{Path: "interrupt/sense.sh", Arguments: []string{}, OnHost: true}
		pushPackage(registrytest.PackageSpec{Uninstall: artifact.UninstallSupported, Interrupt: &artifact.Interrupt{Sense: step, Run: step}})
		Expect(NewResult(inspect("1.0")).Outcome).To(Equal(Outcome{Kind: KindRejected, Reason: ReasonInterrupt}))
	})

	It("cannot inspect a tag the registry does not have, and keeps the error as Detail", func() {
		reference, inspection, err := inspect("2.0")
		r := NewResult(reference, inspection, err)
		Expect(r.Outcome).To(Equal(Outcome{Kind: KindUnknown, ErrorClass: ErrorNotFound}))
		Expect(r.Detail).To(Equal(err.Error()))
		Expect(r.Credential).To(BeEmpty())
	})

	Describe("against a registry that requires credentials", func() {
		right := fmt.Sprintf(`{"username":%q,"password":%q}`, classifyUser, classifyPassword)

		BeforeEach(func() {
			reg = registrytest.New(registrytest.Options{Username: classifyUser, Password: classifyPassword})
			DeferCleanup(reg.Close)
			pushPackage(registrytest.PackageSpec{Uninstall: artifact.UninstallSupported})
		})

		It("cannot inspect anonymously", func() {
			Expect(NewResult(inspect("1.0")).Outcome).To(Equal(Outcome{Kind: KindUnknown, ErrorClass: ErrorUnauthorized}))
		})

		It("reports the source of the credential the registry accepted", func() {
			config := fmt.Sprintf(`{"auths":{%q:%s}}`, reg.Host(), right)
			r := NewResult(inspect("1.0", client.DockerConfigJSON("pull-secret-0", []byte(config))))
			Expect(r.Kind).To(Equal(KindArtifact))
			Expect(r.Credential).To(Equal("pull-secret-0"))
			data, err := Encode(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).NotTo(ContainSubstring(classifyPassword))
		})
	})

	DescribeTable("classifies an error",
		func(err error, want Outcome) {
			Expect(Classify(nil, err)).To(Equal(want))
		},
		Entry("a deadline", fmt.Errorf("resolving: %w", context.DeadlineExceeded),
			Outcome{Kind: KindUnknown, ErrorClass: ErrorTimeout}),
		Entry("an HTTP client timeout, before its transport class",
			fmt.Errorf("%w: %w", client.ErrTransport, &url.Error{Op: "Get", URL: "https://registry.example.com/v2/", Err: &net.DNSError{IsTimeout: true}}),
			Outcome{Kind: KindUnknown, ErrorClass: ErrorTimeout}),
		Entry("a cancellation", fmt.Errorf("resolving: %w", context.Canceled),
			Outcome{Kind: KindUnknown, ErrorClass: ErrorCanceled}),
		Entry("an invalid reference", fmt.Errorf("parsing: %w", client.ErrInvalidReference),
			Outcome{Kind: KindUnknown, ErrorClass: ErrorInvalidReference}),
		Entry("a digest mismatch, before the artifact reason it also matches",
			fmt.Errorf("%w: %w", &artifact.InvalidArtifactError{Reason: artifact.ReasonMetadataMismatch}, client.ErrDigestMismatch),
			Outcome{Kind: KindUnknown, ErrorClass: ErrorDigestMismatch}),
		Entry("a transport failure", fmt.Errorf("%w: connection refused", client.ErrTransport),
			Outcome{Kind: KindUnknown, ErrorClass: ErrorTransport}),
		Entry("a registry that kept answering 503", &errcode.ErrorResponse{StatusCode: http.StatusServiceUnavailable},
			Outcome{Kind: KindUnknown, ErrorClass: ErrorUnavailable}),
		Entry("a registry that kept answering 429", &errcode.ErrorResponse{StatusCode: http.StatusTooManyRequests},
			Outcome{Kind: KindUnknown, ErrorClass: ErrorUnavailable}),
		Entry("a registry that answered 400", &errcode.ErrorResponse{StatusCode: http.StatusBadRequest},
			Outcome{Kind: KindUnknown, ErrorClass: ErrorOther}),
		Entry("any other error", errors.New("something else"),
			Outcome{Kind: KindUnknown, ErrorClass: ErrorOther}),
		Entry("a malformed manifest", fmt.Errorf("classifying: %w", artifact.ErrMalformedManifest),
			Outcome{Kind: KindRejected, Reason: ReasonMalformedManifest}),
		Entry("an unsupported form", fmt.Errorf("classifying: %w", artifact.ErrUnsupportedForm),
			Outcome{Kind: KindRejected, Reason: ReasonUnsupportedForm}),
		Entry("an unsupported version", fmt.Errorf("classifying: %w", artifact.ErrUnsupportedVersion),
			Outcome{Kind: KindRejected, Reason: ReasonUnsupportedVersion}),
		Entry("an invalid artifact, with its reason", fmt.Errorf("parsing: %w", &artifact.InvalidArtifactError{Reason: artifact.ReasonMetadataTooLarge}),
			Outcome{Kind: KindRejected, Reason: ReasonInvalidArtifact + string(artifact.ReasonMetadataTooLarge)}),
	)
})
