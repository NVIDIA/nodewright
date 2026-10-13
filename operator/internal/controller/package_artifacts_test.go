// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("package artifact inspection", func() {
	const (
		namespace  = "nodewright"
		pullSecret = "registry-credentials"
		username   = "test-user"
		password   = "test-password-placeholder"
		version    = "1.0.0"
	)

	var (
		ctx        context.Context
		fakeClock  *testingclock.FakePassiveClock
		nodewright *v1alpha1.NodeWright
		recorder   *events.FakeRecorder
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClock = testingclock.NewFakePassiveClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
		nodewright = &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: "artifacts"}}
		// Verbose so each message carries the action; sized for every event a spec records,
		// since a full channel blocks Eventf.
		recorder = &events.FakeRecorder{Events: make(chan string, 10), Verbose: true}
	})

	newRegistry := func(opts registrytest.Options) *registrytest.Registry {
		reg := registrytest.New(opts)
		DeferCleanup(reg.Close)
		return reg
	}

	// newCache returns a cache that sends its requests through httpClient and reads the pull
	// secret named secretName, if any, from objects.
	newCache := func(httpClient *http.Client, secretName string, objects ...client.Object) *packageArtifactCache {
		c := newPackageArtifactCache(fake.NewClientBuilder().WithObjects(objects...).Build(), recorder, namespace, secretName)
		c.httpClient = httpClient
		c.clock = fakeClock
		return c
	}

	packageAt := func(reg *registrytest.Registry, repository, containerSHA string) *v1alpha1.Package {
		return &v1alpha1.Package{
			PackageRef:   v1alpha1.PackageRef{Name: "pkg", Version: version},
			Image:        reg.Host() + "/" + repository,
			ContainerSHA: containerSHA,
		}
	}

	pinnedAt := func(reg *registrytest.Registry, repository string, desc ocispec.Descriptor) string {
		return reg.Host() + "/" + repository + "@" + desc.Digest.String()
	}

	// cached returns what cache.cached answers, or nil when it reports false.
	cached := func(cache *packageArtifactCache, _package *v1alpha1.Package) *packageArtifactOutcome {
		outcome, ok := cache.cached(_package)
		if !ok {
			return nil
		}
		return &outcome
	}

	stepPast := func(d time.Duration) {
		fakeClock.SetTime(fakeClock.Now().Add(d + time.Second))
	}

	pushPackage := func(reg *registrytest.Registry, repository, tag string, spec registrytest.PackageSpec) ocispec.Descriptor {
		if spec.Uninstall == "" {
			spec.Uninstall = artifact.UninstallSupported
		}
		if spec.Config == nil {
			spec.Config = json.RawMessage(`{}`)
		}
		if spec.Payload == nil {
			spec.Payload = []registrytest.Layer{{Gzip: []byte("payload")}}
		}
		desc, _, err := registrytest.PushPackage(ctx, reg.Repository(repository), tag, spec)
		Expect(err).NotTo(HaveOccurred())
		return desc
	}

	pushManifest := func(reg *registrytest.Registry, repository, mediaType string, manifest any, tag string) ocispec.Descriptor {
		data, err := json.Marshal(manifest)
		Expect(err).NotTo(HaveOccurred())
		desc, err := registrytest.PushManifest(ctx, reg.Repository(repository), mediaType, data, tag)
		Expect(err).NotTo(HaveOccurred())
		return desc
	}

	// pushMetadata pushes the metadata blob pushPackage writes for a default spec, so a corrected
	// package pushed later shares it.
	pushMetadata := func(reg *registrytest.Registry, repository string) ocispec.Descriptor {
		desc, err := registrytest.PushBlob(ctx, reg.Repository(repository), artifact.MediaTypeMetadata, []byte(`{"uninstall":"supported","config":{}}`))
		Expect(err).NotTo(HaveOccurred())
		return desc
	}

	pushNoPayload := func(reg *registrytest.Registry, repository, tag string) ocispec.Descriptor {
		return pushManifest(reg, repository, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			Versioned:    specs.Versioned{SchemaVersion: 2},
			MediaType:    ocispec.MediaTypeImageManifest,
			ArtifactType: artifact.ArtifactTypePackage,
			Config:       pushMetadata(reg, repository),
			Layers:       []ocispec.Descriptor{},
		}, tag)
	}

	pushV2Package := func(reg *registrytest.Registry, repository, tag string) ocispec.Descriptor {
		return pushManifest(reg, repository, ocispec.MediaTypeImageManifest, ocispec.Manifest{
			Versioned:    specs.Versioned{SchemaVersion: 2},
			MediaType:    ocispec.MediaTypeImageManifest,
			ArtifactType: "application/vnd.nvidia.nodewright.package.v2",
			Config:       pushMetadata(reg, repository),
			Layers:       []ocispec.Descriptor{},
		}, tag)
	}

	pushPackageIndex := func(reg *registrytest.Registry, repository, tag string) ocispec.Descriptor {
		return pushManifest(reg, repository, ocispec.MediaTypeImageIndex, ocispec.Index{
			Versioned:    specs.Versioned{SchemaVersion: 2},
			MediaType:    ocispec.MediaTypeImageIndex,
			ArtifactType: artifact.ArtifactTypePackage,
			Manifests:    []ocispec.Descriptor{},
		}, tag)
	}

	imageDir := func() string {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "hello"), []byte("hello"), 0o644)).To(Succeed())
		return dir
	}

	dockerConfigJSON := func(reg *registrytest.Registry) []byte {
		data, err := json.Marshal(map[string]any{
			"auths": map[string]any{reg.Host(): map[string]string{"username": username, "password": password}},
		})
		Expect(err).NotTo(HaveOccurred())
		return data
	}

	unknown := func(class string) packageArtifactOutcome {
		return packageArtifactOutcome{Kind: packageArtifactUnknown, ErrorClass: class}
	}

	rejected := func(reason string) packageArtifactOutcome {
		return packageArtifactOutcome{Kind: packageArtifactRejected, Reason: reason}
	}

	// rejectedRef is rejected(reason) to compare with what cached answers.
	rejectedRef := func(reason string) *packageArtifactOutcome {
		outcome := rejected(reason)
		return &outcome
	}

	Describe("the reconciler accessor", func() {
		validOpts := func() SkyhookOperatorOptions {
			return SkyhookOperatorOptions{
				Namespace:            namespace,
				CopyDirRoot:          "/var/lib/skyhook",
				AgentLogRoot:         "/var/log/skyhook",
				RuntimeRequiredTaint: "skyhook.nvidia.com=runtime-required:NoSchedule",
				AgentImage:           "ghcr.io/nvidia/skyhook/agent:1.2.3",
				PauseImage:           "registry.k8s.io/pause:3.10",
				MaxInterval:          10 * time.Minute,
				JobOperatorOptions: JobOperatorOptions{
					JobTTLSucceeded: time.Hour,
					JobTTLFailed:    24 * time.Hour,
					JobStageTimeout: time.Hour,
				},
			}
		}

		newReconciler := func(opts SkyhookOperatorOptions) *SkyhookReconciler {
			c := fake.NewClientBuilder().Build()
			r, err := NewSkyhookReconciler(runtime.NewScheme(), c, c, k8sfake.NewClientset(), recorder, opts)
			Expect(err).NotTo(HaveOccurred())
			return r
		}

		It("answers disabled with no request while the option is off, even with a cache injected", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			_package := packageAt(reg, "pkg", "")
			opts := validOpts()
			opts.artifactCache = newCache(reg.Client(), "")

			off := newReconciler(opts)
			Expect(off.packageArtifactOutcome(ctx, nodewright, _package)).To(Equal(packageArtifactOutcome{Kind: packageArtifactDisabled}))
			Expect(reg.Requests()).To(BeEmpty())

			opts.PackageArtifacts = true
			on := newReconciler(opts)
			Expect(on.opts.artifactCache).To(BeIdenticalTo(opts.artifactCache))
			outcome := on.packageArtifactOutcome(ctx, nodewright, _package)
			Expect(outcome.Kind).To(Equal(packageArtifactAccepted))
			Expect(outcome.Pinned).To(Equal(pinnedAt(reg, "pkg", desc)))
			Expect(reg.Requests()).NotTo(BeEmpty())
		})

		It("builds a cache only when the option is on", func() {
			opts := validOpts()
			opts.ImagePullSecret = pullSecret
			Expect(newReconciler(opts).opts.artifactCache).To(BeNil())

			opts.PackageArtifacts = true
			cache := newReconciler(opts).opts.artifactCache
			Expect(cache).NotTo(BeNil())
			Expect(cache.namespace).To(Equal(namespace))
			Expect(cache.pullSecret).To(Equal(pullSecret))
		})
	})

	Describe("lookup", func() {
		It("inspects a miss once and answers a second call with no request", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", "")
			Expect(cached(cache, _package)).To(BeNil())

			first := cache.lookup(ctx, nodewright, _package)
			Expect(first.Kind).To(Equal(packageArtifactAccepted))
			Expect(reg.Requests()).NotTo(BeEmpty())

			reg.ResetRequests()
			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(first))
			Expect(reg.Requests()).To(BeEmpty())
			Expect(cached(cache, _package)).To(Equal(&first))
		})

		It("pins a containerSHA package to that digest and never names a tag", func() {
			reg := newRegistry(registrytest.Options{})
			pinned := pushPackage(reg, "pkg", version, registrytest.PackageSpec{Uninstall: artifact.UninstallSupported})
			// The tag the version names moves to another package, so resolving it would answer that one.
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", pinned.Digest.String())

			outcome := cache.lookup(ctx, nodewright, _package)
			Expect(outcome.Kind).To(Equal(packageArtifactAccepted))
			Expect(outcome.Pinned).To(Equal(pinnedAt(reg, "pkg", pinned)))
			Expect(outcome.Package.Uninstall).To(Equal(artifact.UninstallSupported))
			Expect(reg.Requests()).NotTo(BeEmpty())
			for _, request := range reg.Requests() {
				Expect(request.Path).NotTo(HaveSuffix("/" + version))
			}
			Expect(cached(cache, _package)).To(Equal(&outcome))
		})

		It("resolves a tag and pins it", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")

			outcome := cache.lookup(ctx, nodewright, packageAt(reg, "pkg", ""))
			Expect(outcome.Kind).To(Equal(packageArtifactAccepted))
			Expect(outcome.Pinned).To(Equal(pinnedAt(reg, "pkg", desc)))
			Expect(outcome.Package).NotTo(BeNil())
			Expect(outcome.Reason).To(BeEmpty())
			Expect(outcome.ErrorClass).To(BeEmpty())
			// The digest it resolved to answers too.
			Expect(cached(cache, packageAt(reg, "pkg", desc.Digest.String()))).To(Equal(&outcome))
		})

		DescribeTable("answers legacy for an image or an index",
			func(push func(context.Context, registrytest.Pusher, string, string) (ocispec.Descriptor, error)) {
				reg := newRegistry(registrytest.Options{})
				desc, err := push(ctx, reg.Repository("legacy"), version, imageDir())
				Expect(err).NotTo(HaveOccurred())
				cache := newCache(reg.Client(), "")

				Expect(cache.lookup(ctx, nodewright, packageAt(reg, "legacy", ""))).To(Equal(packageArtifactOutcome{
					Kind:   packageArtifactLegacy,
					Pinned: pinnedAt(reg, "legacy", desc),
				}))
			},
			Entry("an image", registrytest.PushImage),
			Entry("an index", registrytest.PushIndex),
		)

		interrupt := &artifact.Interrupt{
			Sense: artifact.InterruptStep{Path: "sense.sh", Arguments: []string{}},
			Run:   artifact.InterruptStep{Path: "run.sh", Arguments: []string{}, OnHost: true},
		}
		hostFiles := []registrytest.HostFilesLayer{{Layer: registrytest.Layer{Gzip: []byte("host files")}, Destination: "/etc/nodewright"}}

		DescribeTable("rejects a package declaring host files or an interrupt, with no Pinned",
			func(spec registrytest.PackageSpec, reason string) {
				reg := newRegistry(registrytest.Options{})
				pushPackage(reg, "pkg", version, spec)
				cache := newCache(reg.Client(), "")
				_package := packageAt(reg, "pkg", "")

				Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(rejected(reason)))
				Expect(cached(cache, _package)).To(Equal(rejectedRef(reason)))
			},
			Entry("host files", registrytest.PackageSpec{HostFiles: hostFiles}, "host-files"),
			Entry("an interrupt", registrytest.PackageSpec{Interrupt: interrupt}, "interrupt"),
			Entry("both, host files first", registrytest.PackageSpec{HostFiles: hostFiles, Interrupt: interrupt}, "host-files"),
		)

		It("accepts a package that declares uninstall unsupported", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			cache := newCache(reg.Client(), "")

			outcome := cache.lookup(ctx, nodewright, packageAt(reg, "pkg", ""))
			Expect(outcome.Kind).To(Equal(packageArtifactAccepted))
			Expect(outcome.Package.Uninstall).To(Equal(artifact.UninstallUnsupported))
		})

		It("answers invalid-reference with no request and stores nothing", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")
			// The webhook refuses a tag inside image; NewReference does too.
			_package := packageAt(reg, "pkg:"+version, "")

			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(unknown("invalid-reference")))
			Expect(reg.Requests()).To(BeEmpty())
			Expect(cached(cache, _package)).To(BeNil())
		})

		It("answers canceled for a done caller context and stores nothing", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", "")
			done, cancel := context.WithCancel(ctx)
			cancel()

			Expect(cache.lookup(done, nodewright, _package)).To(Equal(unknown("canceled")))
			Expect(cached(cache, _package)).To(BeNil())
			// A stored failure would answer this lookup without a request.
			Expect(cache.lookup(ctx, nodewright, _package).Kind).To(Equal(packageArtifactAccepted))
			Expect(reg.Requests()).NotTo(BeEmpty())
		})

		It("answers not-found for a tag the registry does not have", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "pkg", "other", registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", "")

			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(unknown("not-found")))
			Expect(cached(cache, _package)).To(BeNil())
		})

		It("answers digest-mismatch, not a rejection, for a metadata blob that does not match its descriptor", func() {
			reg := newRegistry(registrytest.Options{})
			_, manifest, err := registrytest.PushPackage(ctx, reg.Repository("pkg"), version, registrytest.PackageSpec{
				Uninstall: artifact.UninstallSupported,
				Config:    json.RawMessage(`{}`),
				Payload:   []registrytest.Layer{{Gzip: []byte("payload")}},
			})
			Expect(err).NotTo(HaveOccurred())
			// The tampered blob is valid metadata, so only the integrity check refuses it, and it
			// matches an invalid-artifact metadata-mismatch as well.
			reg.Tamper(manifest.Config.Digest, []byte(`{"uninstall":"unsupported","config":{}}`))
			cache := newCache(reg.Client(), "")

			Expect(cache.lookup(ctx, nodewright, packageAt(reg, "pkg", ""))).To(Equal(unknown("digest-mismatch")))
		})
	})

	Describe("classification errors", func() {
		DescribeTable("are rejections cached under a tag until a corrected push lands past the TTL",
			func(push func(*registrytest.Registry, string, string) ocispec.Descriptor, reason string) {
				reg := newRegistry(registrytest.Options{})
				push(reg, "pkg", version)
				cache := newCache(reg.Client(), "")
				_package := packageAt(reg, "pkg", "")

				Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(rejected(reason)))
				reg.ResetRequests()
				Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(rejected(reason)))
				Expect(reg.Requests()).To(BeEmpty())
				Expect(cached(cache, _package)).To(Equal(rejectedRef(reason)))

				stepPast(packageArtifactTagTTL)
				corrected := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
				outcome := cache.lookup(ctx, nodewright, _package)
				Expect(outcome.Kind).To(Equal(packageArtifactAccepted))
				Expect(outcome.Pinned).To(Equal(pinnedAt(reg, "pkg", corrected)))
				Expect(cached(cache, _package)).To(Equal(&outcome))
			},
			Entry("a manifest with no payload layer", pushNoPayload, "invalid-artifact: no-payload"),
			Entry("a v2 package media type", pushV2Package, "unsupported-version"),
			Entry("an index with the package artifact type", pushPackageIndex, "unsupported-form"),
		)

		It("keeps a rejection through a failed refresh", func() {
			reg := newRegistry(registrytest.Options{})
			pushNoPayload(reg, "pkg", version)
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", "")
			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(rejected("invalid-artifact: no-payload")))

			// Past the TTL the tag is refreshed, and the refresh of a closed registry fails, which
			// alone would answer unknown transport.
			reg.Close()
			stepPast(packageArtifactTagTTL)
			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(rejected("invalid-artifact: no-payload")))
			Expect(cached(cache, _package)).To(Equal(rejectedRef("invalid-artifact: no-payload")))
		})

		It("rejects an invalid manifest named by containerSHA for the process lifetime", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushNoPayload(reg, "pkg", "")
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", desc.Digest.String())

			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(rejected("invalid-artifact: no-payload")))
			Expect(cached(cache, _package)).To(Equal(rejectedRef("invalid-artifact: no-payload")))

			reg.ResetRequests()
			stepPast(packageArtifactTagTTL + packageArtifactFailureBackoff)
			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(rejected("invalid-artifact: no-payload")))
			Expect(reg.Requests()).To(BeEmpty())
		})
	})

	Describe("failures", func() {
		It("answer a stored failure with no request until its window ends", func() {
			// With no Secret, the registry answers 401 and logs every request.
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", "")

			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(unknown("unauthorized")))
			oneInspection := len(reg.Requests())
			Expect(oneInspection).To(BeNumerically(">", 0))
			for range 5 {
				Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(unknown("unauthorized")))
			}
			Expect(reg.Requests()).To(HaveLen(oneInspection))
			Expect(cached(cache, _package)).To(BeNil())

			// Only a timeout or a transport failure stands for its whole host.
			pushPackage(reg, "other", version, registrytest.PackageSpec{})
			Expect(cache.lookup(ctx, nodewright, packageAt(reg, "other", ""))).To(Equal(unknown("unauthorized")))
			Expect(reg.Requests()).To(HaveLen(2 * oneInspection))

			stepPast(packageArtifactFailureBackoff)
			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(unknown("unauthorized")))
			Expect(reg.Requests()).To(HaveLen(3 * oneInspection))
		})

		It("re-resolves a tag re-pushed to a new digest once the TTL lapses", func() {
			reg := newRegistry(registrytest.Options{})
			first := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", "")
			Expect(cache.lookup(ctx, nodewright, _package).Pinned).To(Equal(pinnedAt(reg, "pkg", first)))

			second := pushPackage(reg, "pkg", version, registrytest.PackageSpec{Payload: []registrytest.Layer{{Gzip: []byte("payload 2")}}})
			Expect(second.Digest).NotTo(Equal(first.Digest))
			reg.ResetRequests()
			Expect(cache.lookup(ctx, nodewright, _package).Pinned).To(Equal(pinnedAt(reg, "pkg", first)))
			Expect(reg.Requests()).To(BeEmpty())

			stepPast(packageArtifactTagTTL)
			Expect(cache.lookup(ctx, nodewright, _package).Pinned).To(Equal(pinnedAt(reg, "pkg", second)))
		})

		It("reread the Secret on every refresh and answer a stale resolution with no request inside the window", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pullSecret},
				Type:       corev1.SecretTypeDockerConfigJson,
				Data:       map[string][]byte{corev1.DockerConfigJsonKey: dockerConfigJSON(reg)},
			}
			k8sClient := fake.NewClientBuilder().WithObjects(secret).Build()
			cache := newPackageArtifactCache(k8sClient, recorder, namespace, pullSecret)
			cache.httpClient = reg.Client()
			cache.clock = fakeClock
			_package := packageAt(reg, "pkg", "")
			good := cache.lookup(ctx, nodewright, _package)
			Expect(good.Kind).To(Equal(packageArtifactAccepted))

			// The rotated Secret no longer authenticates, so the refresh past the TTL fails.
			secret.Data[corev1.DockerConfigJsonKey] = []byte(strings.ReplaceAll(string(dockerConfigJSON(reg)), password, "rotated"))
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())
			stepPast(packageArtifactTagTTL)
			reg.ResetRequests()
			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(good))
			requests := reg.Requests()
			Expect(requests).NotTo(BeEmpty())
			Expect(requests[len(requests)-1].Status).To(Equal(http.StatusUnauthorized))

			// Inside the window of that failure the stale resolution answers with no request.
			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(good))
			Expect(reg.Requests()).To(HaveLen(len(requests)))
		})

		It("answer the last good result when a refresh fails", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")
			_package := packageAt(reg, "pkg", "")
			good := cache.lookup(ctx, nodewright, _package)
			Expect(good.Pinned).To(Equal(pinnedAt(reg, "pkg", desc)))

			// A refresh of the closed registry alone would answer unknown transport.
			reg.Close()
			stepPast(packageArtifactTagTTL)
			Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(good))
			Expect(cached(cache, _package)).To(Equal(&good))
		})

		It("cost one timed-out connection per window for a black-holed host, whatever number of references it serves", func() {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).NotTo(HaveOccurred())
			var (
				mu       sync.Mutex
				held     []net.Conn
				closed   bool
				accepted atomic.Int64
			)
			// Accept and hold every connection without ever writing, so a client waits for the
			// TLS handshake until its context gives up.
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					accepted.Add(1)
					mu.Lock()
					if closed {
						_ = conn.Close()
					} else {
						held = append(held, conn)
					}
					mu.Unlock()
				}
			}()
			DeferCleanup(func() {
				_ = listener.Close()
				mu.Lock()
				defer mu.Unlock()
				closed = true
				for _, conn := range held {
					_ = conn.Close()
				}
			})

			reachable := newRegistry(registrytest.Options{})
			pushPackage(reachable, "pkg", version, registrytest.PackageSpec{})
			// The client's own timeout is longer than the request timeout, so the context deadline
			// decides.
			cache := newCache(&http.Client{Transport: reachable.Client().Transport, Timeout: time.Minute}, "")
			cache.requestTimeout = 100 * time.Millisecond

			host := listener.Addr().String()
			blackHoled := []*v1alpha1.Package{
				{PackageRef: v1alpha1.PackageRef{Version: version}, Image: host + "/a"},
				{PackageRef: v1alpha1.PackageRef{Version: version}, Image: host + "/b"},
				{PackageRef: v1alpha1.PackageRef{Version: version}, Image: host + "/c"},
				{PackageRef: v1alpha1.PackageRef{Version: version}, Image: host + "/d", ContainerSHA: "sha256:" + strings.Repeat("ab", 32)},
			}
			lookUpAll := func() {
				for range 3 {
					for _, _package := range blackHoled {
						Expect(cache.lookup(ctx, nodewright, _package)).To(Equal(unknown("timeout")), fmt.Sprintf("looking up %s", _package.Image))
					}
				}
			}

			lookUpAll()
			Eventually(accepted.Load).Should(Equal(int64(1)))
			Consistently(accepted.Load, 300*time.Millisecond, 20*time.Millisecond).Should(Equal(int64(1)))

			// The memo is per host: another registry is still asked in the same window.
			cache.lookup(ctx, nodewright, packageAt(reachable, "pkg", ""))
			Expect(reachable.Requests()).NotTo(BeEmpty())

			stepPast(packageArtifactFailureBackoff)
			lookUpAll()
			Eventually(accepted.Load).Should(Equal(int64(2)))
			Consistently(accepted.Load, 300*time.Millisecond, 20*time.Millisecond).Should(Equal(int64(2)))
		})
	})

	Describe("credentials", func() {
		It("authenticate with a dockerconfigjson Secret", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pullSecret},
				Type:       corev1.SecretTypeDockerConfigJson,
				Data:       map[string][]byte{corev1.DockerConfigJsonKey: dockerConfigJSON(reg)},
			}
			cache := newCache(reg.Client(), pullSecret, secret)

			Expect(cache.lookup(ctx, nodewright, packageAt(reg, "pkg", "")).Kind).To(Equal(packageArtifactAccepted))
			Expect(recorder.Events).To(BeEmpty())
		})

		It("are anonymous with no Secret configured", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache(reg.Client(), "")

			Expect(cache.lookup(ctx, nodewright, packageAt(reg, "pkg", ""))).To(Equal(unknown("unauthorized")))
			Expect(recorder.Events).To(BeEmpty())
		})

		// Each unusable Secret carries the registry's credentials where it carries any, so only
		// an anonymous inspection answers unauthorized.
		DescribeTable("record one Warning and inspect anonymously when the Secret is unusable",
			func(secret func(dockerConfig []byte) *corev1.Secret, action string) {
				reg := newRegistry(registrytest.Options{Username: username, Password: password})
				pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
				var objects []client.Object
				if s := secret(dockerConfigJSON(reg)); s != nil {
					objects = append(objects, s)
				}
				cache := newCache(reg.Client(), pullSecret, objects...)

				Expect(cache.lookup(ctx, nodewright, packageAt(reg, "pkg", ""))).To(Equal(unknown("unauthorized")))
				Expect(reg.Requests()).NotTo(BeEmpty())
				Expect(recorder.Events).To(HaveLen(1))
				event := <-recorder.Events
				Expect(event).To(HavePrefix("Warning PackageArtifactPullSecret " + action + " "))
				Expect(event).To(ContainSubstring(namespace + "/" + pullSecret))
				Expect(event).NotTo(ContainSubstring(password))
			},
			Entry("a missing Secret", func([]byte) *corev1.Secret { return nil }, "SecretUnreadable"),
			Entry("an Opaque Secret", func(dockerConfig []byte) *corev1.Secret {
				return &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pullSecret},
					Type:       corev1.SecretTypeOpaque,
					Data:       map[string][]byte{corev1.DockerConfigJsonKey: dockerConfig},
				}
			}, "SecretWrongType"),
			Entry("a dockerconfigjson Secret without its key", func(dockerConfig []byte) *corev1.Secret {
				return &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pullSecret},
					Type:       corev1.SecretTypeDockerConfigJson,
					Data:       map[string][]byte{corev1.DockerConfigKey: dockerConfig},
				}
			}, "SecretMissingKey"),
		)
	})
})
