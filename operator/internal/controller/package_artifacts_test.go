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
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"
	"github.com/NVIDIA/nodewright/operator/internal/wrapper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/events"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var _ = Describe("package artifact inspection", func() {
	const (
		namespace  = "nodewright"
		pullSecret = "registry-credentials"
		username   = "test-user"
		password   = "test-password-placeholder"
		version    = "1.0.0"
		nwName     = "artifacts"
	)

	var (
		ctx       context.Context
		fakeClock *testingclock.FakePassiveClock
		recorder  *events.FakeRecorder
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClock = testingclock.NewFakePassiveClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
		// Verbose so each message carries the action; sized for every event a spec records,
		// since a full channel blocks Eventf.
		recorder = &events.FakeRecorder{Events: make(chan string, 100), Verbose: true}
	})

	newRegistry := func(opts registrytest.Options) *registrytest.Registry {
		reg := registrytest.New(opts)
		DeferCleanup(reg.Close)
		return reg
	}

	// frontOf serves the requests of reg through a TLS server of its own, so on a host of its
	// own, which a spec can fail: intercept answers a request itself when it reports true, and
	// wrap, when set, wraps the server's listener.
	frontOf := func(reg *registrytest.Registry, intercept func(http.ResponseWriter, *http.Request) bool, wrap func(net.Listener) net.Listener) *httptest.Server {
		proxied := reg.Client()
		front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if intercept != nil && intercept(w, r) {
				return
			}
			forward := r.Clone(r.Context())
			forward.RequestURI = ""
			forward.URL.Scheme = "https"
			forward.URL.Host = reg.Host()
			resp, err := proxied.Transport.RoundTrip(forward)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			maps.Copy(w.Header(), resp.Header)
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
		}))
		if wrap != nil {
			front.Listener = wrap(front.Listener)
		}
		front.StartTLS()
		DeferCleanup(front.Close)
		return front
	}

	newScheme := func() *runtime.Scheme {
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(batchv1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		return scheme
	}

	newCache := func() *packageArtifactCache {
		GinkgoHelper()
		cache, err := newPackageArtifactCache(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		return cache
	}

	// tester is an inspector, the client it reads Secrets and the NodeWright nwName through, and
	// that NodeWright, which its events regard.
	type tester struct {
		i  *packageArtifactInspector
		c  client.Client
		nw *v1alpha1.NodeWright
	}

	newTester := func(httpClient *http.Client, secretNames []string, cache *packageArtifactCache, objects ...client.Object) tester {
		GinkgoHelper()
		nw := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: nwName}}
		objects = append(objects, nw)
		c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objects...).WithStatusSubresource(&v1alpha1.NodeWright{}).Build()
		inspector := newPackageArtifactInspector(c, recorder, namespace, secretNames, cache)
		inspector.httpClient = httpClient
		inspector.clock = fakeClock
		return tester{i: inspector, c: c, nw: nw}
	}

	// resolveOf is what one pass resolves of _package.
	resolveOf := func(t tester, _package *v1alpha1.Package) packageArtifactOutcome {
		return t.i.resolve(ctx, t.nw, _package)
	}

	// inspected is what a pass resolves of _package once the inspection a first pass queued is done.
	inspected := func(t tester, _package *v1alpha1.Package) packageArtifactOutcome {
		outcome := resolveOf(t, _package)
		if t.i.queue.Len() == 0 {
			return outcome
		}
		t.i.drain(ctx)
		return resolveOf(t, _package)
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

	stepPast := func(d time.Duration) {
		fakeClock.SetTime(fakeClock.Now().Add(d + time.Second))
	}

	requests := func(reg *registrytest.Registry, method, kind string) []registrytest.Request {
		var matched []registrytest.Request
		for _, r := range reg.Requests() {
			if strings.Contains(r.Path, "/"+kind+"/") && (method == "" || r.Method == method) {
				matched = append(matched, r)
			}
		}
		return matched
	}

	pushPackage := func(reg *registrytest.Registry, repository, tag string, spec registrytest.PackageSpec) ocispec.Descriptor {
		GinkgoHelper()
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
		GinkgoHelper()
		data, err := json.Marshal(manifest)
		Expect(err).NotTo(HaveOccurred())
		desc, err := registrytest.PushManifest(ctx, reg.Repository(repository), mediaType, data, tag)
		Expect(err).NotTo(HaveOccurred())
		return desc
	}

	pushMetadata := func(reg *registrytest.Registry, repository string) ocispec.Descriptor {
		GinkgoHelper()
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

	dockerConfigJSON := func(reg *registrytest.Registry, secretPassword string) []byte {
		data, err := json.Marshal(map[string]any{
			"auths": map[string]any{reg.Host(): map[string]string{"username": username, "password": secretPassword}},
		})
		Expect(err).NotTo(HaveOccurred())
		return data
	}

	pullSecretNamed := func(name string, data []byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: data},
		}
	}

	drainEvents := func() []string {
		var got []string
		for {
			select {
			case event := <-recorder.Events:
				got = append(got, event)
			default:
				return got
			}
		}
	}

	warningsOf := func(events []string, reason string) []string {
		var got []string
		for _, event := range events {
			if strings.HasPrefix(event, "Warning "+reason+" ") {
				got = append(got, event)
			}
		}
		return got
	}

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

	digestOf := func(hex string) string { return "sha256:" + strings.Repeat(hex, 64) }

	Describe("the cache", func() {
		It("keeps one file per digest, renamed into place whole, and reads back what it was given", func() {
			dir := GinkgoT().TempDir()
			cache, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			entry := packageArtifactCacheEntry{Kind: packageArtifactAccepted, Uninstall: artifact.UninstallUnsupported, InspectedAt: fakeClock.Now()}

			Expect(cache.put(ctx, digestOf("a"), entry)).To(Succeed())
			got, ok, err := cache.get(digestOf("a"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(got.Kind).To(Equal(packageArtifactAccepted))
			Expect(got.Uninstall).To(Equal(artifact.UninstallUnsupported))
			Expect(got.InspectedAt).To(BeTemporally("==", fakeClock.Now()))

			files, err := os.ReadDir(filepath.Join(dir, "sha256"))
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(HaveLen(1))
			Expect(files[0].Name()).To(Equal(strings.Repeat("a", 64) + ".json"))

			_, ok, err = cache.get(digestOf("b"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})

		It("empties itself and writes again when a write fails, as once its memory-backed volume is full", func() {
			dir := GinkgoT().TempDir()
			cache, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			entry := packageArtifactCacheEntry{Kind: packageArtifactImage, InspectedAt: fakeClock.Now()}
			Expect(cache.put(ctx, digestOf("a"), entry)).To(Succeed())
			Expect(cache.put(ctx, digestOf("b"), entry)).To(Succeed())
			full := 1
			cache.createTemp = func(dir, pattern string) (*os.File, error) {
				if full > 0 {
					full--
					return nil, &os.PathError{Op: "open", Path: dir, Err: syscall.ENOSPC}
				}
				return os.CreateTemp(dir, pattern)
			}

			Expect(cache.put(ctx, digestOf("c"), entry)).To(Succeed())
			_, ok, err := cache.get(digestOf("c"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			files, err := os.ReadDir(filepath.Join(dir, "sha256"))
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(HaveLen(1))

			// A write that fails again is an error, and leaves no entry.
			full = 2
			Expect(cache.put(ctx, digestOf("d"), entry)).NotTo(Succeed())
			_, ok, err = cache.get(digestOf("d"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})

		It("never empties itself under a write another worker has in flight", func() {
			cache, err := newPackageArtifactCache(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			entry := packageArtifactCacheEntry{Kind: packageArtifactImage, InspectedAt: fakeClock.Now()}
			var calls atomic.Int32
			created, release := make(chan struct{}), make(chan struct{})
			cache.createTemp = func(dir, pattern string) (*os.File, error) {
				switch calls.Add(1) {
				case 1:
					// The first write holds its file open, as a worker between create and rename.
					file, err := os.CreateTemp(dir, pattern)
					close(created)
					<-release
					return file, err
				case 2:
					return nil, &os.PathError{Op: "open", Path: dir, Err: syscall.ENOSPC}
				}
				return os.CreateTemp(dir, pattern)
			}

			first, second := make(chan error, 1), make(chan error, 1)
			go func() { first <- cache.put(ctx, digestOf("a"), entry) }()
			Eventually(created).Should(BeClosed())
			go func() { second <- cache.put(ctx, digestOf("b"), entry) }()
			Consistently(second, 200*time.Millisecond).ShouldNot(Receive())

			close(release)
			Eventually(first).Should(Receive(BeNil()))
			Eventually(second).Should(Receive(BeNil()))
			_, ok, err := cache.get(digestOf("b"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
		})

		DescribeTable("refuses a digest that names anything but a file of its own",
			func(digest string) {
				dir := GinkgoT().TempDir()
				cache, err := newPackageArtifactCache(dir)
				Expect(err).NotTo(HaveOccurred())
				Expect(cache.put(ctx, digest, packageArtifactCacheEntry{Kind: packageArtifactImage})).NotTo(Succeed())
				_, ok, err := cache.get(digest)
				Expect(err).To(HaveOccurred())
				Expect(ok).To(BeFalse())
				files, err := os.ReadDir(filepath.Join(dir, "sha256"))
				Expect(err).NotTo(HaveOccurred())
				Expect(files).To(BeEmpty())
			},
			Entry("a path out of the directory", "sha256:../../../etc/passwd"),
			Entry("uppercase hexadecimal", "sha256:"+strings.Repeat("A", 64)),
			Entry("sha512", "sha512:"+strings.Repeat("a", 128)),
			Entry("a short digest", "sha256:"+strings.Repeat("a", 63)),
			Entry("a digest with a path after it", "sha256:"+strings.Repeat("a", 64)+"/x"),
			Entry("nothing", ""),
		)

		It("holds only what a digest is, never a reference that could not be read", func() {
			cache := newCache()
			for _, kind := range []packageArtifactKind{packageArtifactUnreadable, packageArtifactUnavailable, packageArtifactPending, packageArtifactDisabled, packageArtifactUnresolved} {
				Expect(cache.put(ctx, digestOf("a"), packageArtifactCacheEntry{Kind: kind})).NotTo(Succeed(), string(kind))
			}
			_, ok, err := cache.get(digestOf("a"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})

		It("answers a corrupt entry, or one of another kind, with an error", func() {
			dir := GinkgoT().TempDir()
			cache, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			for _, content := range []string{"not json", `{"kind":"unknown"}`} {
				Expect(os.WriteFile(filepath.Join(dir, "sha256", strings.Repeat("a", 64)+".json"), []byte(content), 0o600)).To(Succeed())
				_, ok, err := cache.get(digestOf("a"))
				Expect(err).To(HaveOccurred(), content)
				Expect(ok).To(BeFalse(), content)
			}
		})

		It("creates its directory, and refuses one it cannot write in", func() {
			dir := filepath.Join(GinkgoT().TempDir(), "nested", "cache")
			_, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(dir, "sha256")).To(BeADirectory())

			file := filepath.Join(GinkgoT().TempDir(), "file")
			Expect(os.WriteFile(file, nil, 0o600)).To(Succeed())
			_, err = newPackageArtifactCache(file)
			Expect(err).To(HaveOccurred())

			readOnly := GinkgoT().TempDir()
			Expect(os.Mkdir(filepath.Join(readOnly, "sha256"), 0o500)).To(Succeed())
			DeferCleanup(os.Chmod, filepath.Join(readOnly, "sha256"), os.FileMode(0o700))
			_, err = newPackageArtifactCache(readOnly)
			Expect(err).To(HaveOccurred())

			_, err = newPackageArtifactCache("")
			Expect(err).To(HaveOccurred())
		})
	})

	It("waits for an unreadable reference packageArtifactRetry doubled per failure in a row, up to packageArtifactRetryMax", func() {
		Expect(packageArtifactRetryAfter(0)).To(Equal(5 * time.Minute))
		Expect(packageArtifactRetryAfter(1)).To(Equal(5 * time.Minute))
		Expect(packageArtifactRetryAfter(2)).To(Equal(10 * time.Minute))
		Expect(packageArtifactRetryAfter(3)).To(Equal(20 * time.Minute))
		Expect(packageArtifactRetryAfter(4)).To(Equal(40 * time.Minute))
		Expect(packageArtifactRetryAfter(5)).To(Equal(time.Hour))
		Expect(packageArtifactRetryAfter(40)).To(Equal(time.Hour))
	})

	DescribeTable("withholds and refuses a stage Job by what the pass resolved",
		func(outcome packageArtifactOutcome, stage v1alpha1.Stage, withheld, refused bool) {
			Expect(stageJobWithheld(outcome, stage)).To(Equal(withheld))
			Expect(stageJobRefused(outcome, stage)).To(Equal(refused))
		},
		Entry("pending: withheld for now", packageArtifactOutcome{Kind: packageArtifactPending}, v1alpha1.StageApply, true, false),
		Entry("unavailable: withheld for now", packageArtifactOutcome{Kind: packageArtifactUnavailable, ErrorClass: "timeout"}, v1alpha1.StageApply, true, false),
		Entry("unreadable: refused", packageArtifactOutcome{Kind: packageArtifactUnreadable, ErrorClass: "unauthorized"}, v1alpha1.StageApply, true, true),
		Entry("rejected: refused", rejectedPackageArtifact("host-files"), v1alpha1.StageConfig, true, true),
		Entry("an artifact declaring uninstall unsupported, at uninstall: refused",
			packageArtifactOutcome{Kind: packageArtifactAccepted, Uninstall: artifact.UninstallUnsupported}, v1alpha1.StageUninstall, true, true),
		Entry("an artifact declaring uninstall unsupported, at apply: run",
			packageArtifactOutcome{Kind: packageArtifactAccepted, Uninstall: artifact.UninstallUnsupported}, v1alpha1.StageApply, false, false),
		Entry("an image: run", packageArtifactOutcome{Kind: packageArtifactImage}, v1alpha1.StageUninstall, false, false),
		Entry("the option off: run", packageArtifactOutcome{Kind: packageArtifactDisabled}, v1alpha1.StageApply, false, false),
		Entry("nothing resolved: run", packageArtifactOutcome{Kind: packageArtifactUnresolved}, v1alpha1.StageApply, false, false),
	)

	Describe("building the inspector", func() {
		newReconciler := func(opts SkyhookOperatorOptions) (*SkyhookReconciler, error) {
			c := fake.NewClientBuilder().Build()
			return NewSkyhookReconciler(runtime.NewScheme(), c, c, k8sfake.NewClientset(), recorder, opts)
		}

		It("builds one only with the option on, with every pull Secret in order and the cache in PACKAGE_ARTIFACT_CACHE_DIR", func() {
			dir := filepath.Join(GinkgoT().TempDir(), "cache")
			opts := validOpts()
			opts.ImagePullSecret = pullSecret
			opts.ImagePullSecrets = []string{"second"}
			opts.PackageArtifactCacheDir = dir
			r, err := newReconciler(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.opts.artifactInspector).To(BeNil())
			Expect(dir).NotTo(BeAnExistingFile())

			opts.PackageArtifacts = true
			r, err = newReconciler(opts)
			Expect(err).NotTo(HaveOccurred())
			inspector := r.opts.artifactInspector
			Expect(inspector).NotTo(BeNil())
			Expect(inspector.namespace).To(Equal(namespace))
			Expect(inspector.pullSecrets).To(Equal([]string{pullSecret, "second"}))
			Expect(inspector.cache.dir).To(Equal(dir))
			Expect(filepath.Join(dir, "sha256")).To(BeADirectory())
		})

		It("fails, with the option on, when it cannot write in the cache directory", func() {
			file := filepath.Join(GinkgoT().TempDir(), "file")
			Expect(os.WriteFile(file, nil, 0o600)).To(Succeed())
			opts := validOpts()
			opts.PackageArtifacts = true
			opts.PackageArtifactCacheDir = file
			_, err := newReconciler(opts)
			Expect(err).To(MatchError(ContainSubstring("PACKAGE_ARTIFACT_CACHE_DIR")))
		})
	})

	Describe("resolve", func() {
		It("resolves a tag with one manifest request, queues the digest it names, and answers what its inspection cached, pinned to it", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			t := newTester(reg.Client(), nil, newCache())
			_package := packageAt(reg, "pkg", "")
			reg.ResetRequests()

			Expect(resolveOf(t, _package)).To(Equal(packageArtifactOutcome{Kind: packageArtifactPending, Pinned: pinnedAt(reg, "pkg", desc)}))
			Expect(requests(reg, http.MethodHead, "manifests")).To(HaveLen(1))
			Expect(requests(reg, http.MethodGet, "manifests")).To(BeEmpty())
			Expect(t.i.queue.Len()).To(Equal(1))

			t.i.drain(ctx)
			reg.ResetRequests()
			stepPast(packageArtifactResolvedFor)
			Expect(resolveOf(t, _package)).To(Equal(packageArtifactOutcome{
				Kind: packageArtifactAccepted, Pinned: pinnedAt(reg, "pkg", desc), Uninstall: artifact.UninstallUnsupported,
				RetryAt: fakeClock.Now().Add(packageArtifactResolvedFor),
			}))
			Expect(requests(reg, http.MethodHead, "manifests")).To(HaveLen(1))
			Expect(requests(reg, http.MethodGet, "manifests")).To(BeEmpty())
			Expect(requests(reg, "", "blobs")).To(BeEmpty())
			Expect(t.i.queue.Len()).To(BeZero())
		})

		It("sends no request to resolve a containerSHA, and reads only its manifest and metadata to inspect it", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), nil, newCache())
			_package := packageAt(reg, "pkg", desc.Digest.String())
			reg.ResetRequests()

			Expect(resolveOf(t, _package).Kind).To(Equal(packageArtifactPending))
			Expect(reg.Requests()).To(BeEmpty())

			t.i.drain(ctx)
			Expect(reg.Requests()).NotTo(BeEmpty())
			for _, request := range reg.Requests() {
				Expect(request.Path).NotTo(HaveSuffix("/" + version))
			}
			Expect(requests(reg, http.MethodGet, "blobs")).To(HaveLen(1))

			reg.ResetRequests()
			Expect(resolveOf(t, _package)).To(Equal(packageArtifactOutcome{
				Kind: packageArtifactAccepted, Pinned: pinnedAt(reg, "pkg", desc), Uninstall: artifact.UninstallSupported,
			}))
			Expect(reg.Requests()).To(BeEmpty())
		})

		It("resolves a tag once per packageArtifactResolvedFor, whatever number of packages, NodeWrights and passes name it", func() {
			reg := newRegistry(registrytest.Options{})
			first := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), nil, newCache())
			_package := packageAt(reg, "pkg", "")
			named := packageAt(reg, "pkg", "")
			named.Name = "named"
			other := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: "other"}}
			resolved := fakeClock.Now()
			reg.ResetRequests()

			Expect(resolveOf(t, _package).Pinned).To(Equal(pinnedAt(reg, "pkg", first)))
			Expect(resolveOf(t, named).Pinned).To(Equal(pinnedAt(reg, "pkg", first)))
			Expect(t.i.resolve(ctx, other, _package).Pinned).To(Equal(pinnedAt(reg, "pkg", first)))
			Expect(requests(reg, http.MethodHead, "manifests")).To(HaveLen(1))

			t.i.drain(ctx)
			second := pushPackage(reg, "pkg", version, registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			reg.ResetRequests()
			fakeClock.SetTime(resolved.Add(packageArtifactResolvedFor - time.Second))
			// Read again once the digest stops answering for the tag, so the pass runs by then.
			Expect(resolveOf(t, _package)).To(Equal(packageArtifactOutcome{
				Kind: packageArtifactAccepted, Pinned: pinnedAt(reg, "pkg", first), Uninstall: artifact.UninstallSupported,
				RetryAt: resolved.Add(packageArtifactResolvedFor),
			}))
			Expect(requests(reg, http.MethodHead, "manifests")).To(BeEmpty())

			fakeClock.SetTime(resolved.Add(packageArtifactResolvedFor))
			Expect(resolveOf(t, named).Pinned).To(Equal(pinnedAt(reg, "pkg", second)))
			Expect(resolveOf(t, _package).Pinned).To(Equal(pinnedAt(reg, "pkg", second)))
			Expect(requests(reg, http.MethodHead, "manifests")).To(HaveLen(1))
		})

		It("follows a tag re-pushed to another digest once its resolution is packageArtifactResolvedFor old, and keeps what the first digest is", func() {
			reg := newRegistry(registrytest.Options{})
			first := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), nil, newCache())
			_package := packageAt(reg, "pkg", "")
			Expect(inspected(t, _package).Pinned).To(Equal(pinnedAt(reg, "pkg", first)))

			second := pushPackage(reg, "pkg", version, registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			stepPast(packageArtifactResolvedFor)
			Expect(resolveOf(t, _package)).To(Equal(packageArtifactOutcome{Kind: packageArtifactPending, Pinned: pinnedAt(reg, "pkg", second)}))
			Expect(inspected(t, _package)).To(Equal(packageArtifactOutcome{
				Kind: packageArtifactAccepted, Pinned: pinnedAt(reg, "pkg", second), Uninstall: artifact.UninstallUnsupported,
				RetryAt: fakeClock.Now().Add(packageArtifactResolvedFor),
			}))

			reg.ResetRequests()
			Expect(resolveOf(t, packageAt(reg, "pkg", first.Digest.String())).Uninstall).To(Equal(artifact.UninstallSupported))
			Expect(reg.Requests()).To(BeEmpty())
		})

		DescribeTable("caches a container image, pinned to its digest",
			func(push func(context.Context, registrytest.Pusher, string, string) (ocispec.Descriptor, error)) {
				reg := newRegistry(registrytest.Options{})
				desc, err := push(ctx, reg.Repository("pkg"), version, imageDir())
				Expect(err).NotTo(HaveOccurred())
				t := newTester(reg.Client(), nil, newCache())

				Expect(inspected(t, packageAt(reg, "pkg", ""))).To(Equal(packageArtifactOutcome{
					Kind: packageArtifactImage, Pinned: pinnedAt(reg, "pkg", desc), RetryAt: fakeClock.Now().Add(packageArtifactResolvedFor),
				}))
				Expect(drainEvents()).To(BeEmpty())
			},
			Entry("an image", registrytest.PushImage),
			Entry("an index", registrytest.PushIndex),
		)

		hostFiles := []registrytest.HostFilesLayer{{Layer: registrytest.Layer{Gzip: []byte("host files")}, Destination: "/etc/nodewright"}}
		interrupt := &artifact.Interrupt{
			Sense: artifact.InterruptStep{Path: "sense.sh", Arguments: []string{}},
			Run:   artifact.InterruptStep{Path: "run.sh", Arguments: []string{}, OnHost: true},
		}

		DescribeTable("caches what this operator does not run as rejected, with a Warning once per digest",
			func(push func(reg *registrytest.Registry), reason string) {
				reg := newRegistry(registrytest.Options{})
				push(reg)
				t := newTester(reg.Client(), nil, newCache())
				_package := packageAt(reg, "pkg", "")

				Expect(inspected(t, _package)).To(HaveField("Reason", reason))
				Expect(resolveOf(t, _package).Kind).To(Equal(packageArtifactRejected))
				warnings := warningsOf(drainEvents(), EventsReasonPackageArtifactRejected)
				Expect(warnings).To(HaveLen(1))
				Expect(warnings[0]).To(HavePrefix("Warning PackageArtifactRejected pkg:" + version + " "))
				Expect(warnings[0]).To(ContainSubstring(reason))

				reg.ResetRequests()
				Expect(resolveOf(t, _package).Reason).To(Equal(reason))
				Expect(requests(reg, http.MethodGet, "manifests")).To(BeEmpty())
				Expect(drainEvents()).To(BeEmpty())
			},
			Entry("host files", func(reg *registrytest.Registry) {
				pushPackage(reg, "pkg", version, registrytest.PackageSpec{HostFiles: hostFiles})
			}, "host-files"),
			Entry("an interrupt", func(reg *registrytest.Registry) {
				pushPackage(reg, "pkg", version, registrytest.PackageSpec{Interrupt: interrupt})
			}, "interrupt"),
			Entry("both, host files first", func(reg *registrytest.Registry) {
				pushPackage(reg, "pkg", version, registrytest.PackageSpec{HostFiles: hostFiles, Interrupt: interrupt})
			}, "host-files"),
			Entry("a manifest with no payload layer", func(reg *registrytest.Registry) { pushNoPayload(reg, "pkg", version) }, "invalid-artifact: no-payload"),
			Entry("a v2 package media type", func(reg *registrytest.Registry) { pushV2Package(reg, "pkg", version) }, "unsupported-version"),
			Entry("an index with the package artifact type", func(reg *registrytest.Registry) { pushPackageIndex(reg, "pkg", version) }, "unsupported-form"),
		)

		It("blocks a digest whose metadata does not match its descriptor, digest-mismatch, and caches nothing", func() {
			reg := newRegistry(registrytest.Options{})
			_, manifest, err := registrytest.PushPackage(ctx, reg.Repository("pkg"), version, registrytest.PackageSpec{
				Uninstall: artifact.UninstallSupported,
				Config:    json.RawMessage(`{}`),
				Payload:   []registrytest.Layer{{Gzip: []byte("payload")}},
			})
			Expect(err).NotTo(HaveOccurred())
			// The tampered blob is valid metadata, so only the integrity check refuses it.
			reg.Tamper(manifest.Config.Digest, []byte(`{"uninstall":"unsupported","config":{}}`))
			dir := GinkgoT().TempDir()
			cache, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			t := newTester(reg.Client(), nil, cache)

			outcome := inspected(t, packageAt(reg, "pkg", ""))
			Expect(outcome.Kind).To(Equal(packageArtifactUnreadable))
			Expect(outcome.ErrorClass).To(Equal("digest-mismatch"))
			Expect(outcome.RetryAt).To(BeTemporally("==", fakeClock.Now().Add(packageArtifactRetry)))
			Expect(os.ReadDir(filepath.Join(dir, "sha256"))).To(BeEmpty())
		})

		It("blocks a tag the registry does not have with a Warning once, and reads it again after packageArtifactRetry, doubled per failure", func() {
			reg := newRegistry(registrytest.Options{})
			t := newTester(reg.Client(), nil, newCache())
			_package := packageAt(reg, "pkg", "")

			outcome := resolveOf(t, _package)
			Expect(outcome.Kind).To(Equal(packageArtifactUnreadable))
			Expect(outcome.ErrorClass).To(Equal("not-found"))
			Expect(outcome.RetryAt).To(BeTemporally("==", fakeClock.Now().Add(packageArtifactRetry)))
			warnings := warningsOf(drainEvents(), EventsReasonPackageArtifactInspectionFailed)
			Expect(warnings).To(HaveLen(1))
			Expect(warnings[0]).To(HavePrefix("Warning PackageArtifactInspectionFailed pkg:" + version + " "))
			Expect(warnings[0]).To(ContainSubstring("blocked"))
			Expect(warnings[0]).To(ContainSubstring("imagePullSecrets"))

			reg.ResetRequests()
			Expect(resolveOf(t, _package).ErrorClass).To(Equal("not-found"))
			Expect(reg.Requests()).To(BeEmpty())

			stepPast(packageArtifactRetry)
			outcome = resolveOf(t, _package)
			Expect(outcome.ErrorClass).To(Equal("not-found"))
			Expect(outcome.RetryAt).To(BeTemporally("==", fakeClock.Now().Add(2*packageArtifactRetry)))
			Expect(reg.Requests()).NotTo(BeEmpty())
			Expect(drainEvents()).To(BeEmpty())

			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			stepPast(2 * packageArtifactRetry)
			Expect(inspected(t, _package).Pinned).To(Equal(pinnedAt(reg, "pkg", desc)))
		})

		It("blocks a package whose reference does not parse, with no request and nothing queued", func() {
			reg := newRegistry(registrytest.Options{})
			t := newTester(reg.Client(), nil, newCache())
			invalid := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "bad", Version: version}, Image: "Not A Reference"}

			Expect(resolveOf(t, invalid)).To(Equal(packageArtifactOutcome{Kind: packageArtifactUnreadable, ErrorClass: "invalid-reference"}))
			Expect(reg.Requests()).To(BeEmpty())
			Expect(t.i.queue.Len()).To(BeZero())
		})

		// Each would fail the same way at every attempt, so the package is blocked rather than waiting
		// for it forever.
		DescribeTable("blocks a registry the operator cannot read for a reason that does not pass by itself, by tag and by digest",
			func(serve func() (string, *http.Client), class string) {
				host, httpClient := serve()
				t := newTester(httpClient, nil, newCache())
				for _, _package := range []*v1alpha1.Package{
					{PackageRef: v1alpha1.PackageRef{Name: "tagged", Version: version}, Image: host + "/pkg"},
					{PackageRef: v1alpha1.PackageRef{Name: "pinned", Version: version}, Image: host + "/pkg", ContainerSHA: "sha256:" + strings.Repeat("ab", 32)},
				} {
					outcome := inspected(t, _package)
					Expect(outcome.Kind).To(Equal(packageArtifactUnreadable), _package.Name)
					Expect(outcome.ErrorClass).To(Equal(class), _package.Name)
					Expect(outcome.RetryAt).To(BeTemporally("==", fakeClock.Now().Add(packageArtifactRetry)), _package.Name)
				}
			},
			Entry("a certificate the operator does not trust", func() (string, *http.Client) {
				return newRegistry(registrytest.Options{}).Host(), &http.Client{Transport: &http.Transport{}}
			}, "tls"),
			Entry("a server that speaks plain HTTP", func() (string, *http.Client) {
				server := httptest.NewServer(http.NotFoundHandler())
				DeferCleanup(server.Close)
				return server.Listener.Addr().String(), &http.Client{Transport: &http.Transport{}}
			}, "plain-http"),
			Entry("a host name that does not resolve", func() (string, *http.Client) {
				return "registry.nodewright.invalid", &http.Client{Transport: &http.Transport{
					DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
						return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: address, IsNotFound: true}}
					},
				}}
			}, "no-such-host"),
			Entry("an error status about the request itself", func() (string, *http.Client) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
				}))
				DeferCleanup(server.Close)
				return server.Listener.Addr().String(), server.Client()
			}, "refused"),
		)

		It("announces what an artifact declares about uninstall once per digest, and what opting in does", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), nil, newCache())
			_package := packageAt(reg, "pkg", "")
			t.nw.Spec.Packages = v1alpha1.Packages{_package.Name: *_package}
			Expect(t.c.Update(ctx, t.nw)).To(Succeed())

			inspected(t, _package)
			events := drainEvents()
			Expect(events).To(HaveLen(1))
			Expect(events[0]).To(HavePrefix("Normal UninstallCapability pkg@" + desc.Digest.String() + " "))
			Expect(events[0]).To(ContainSubstring("declares uninstall supported"))
			Expect(events[0]).To(ContainSubstring("setting uninstall.enabled to true opts the package in"))

			resolveOf(t, _package)
			resolveOf(t, packageAt(reg, "pkg", desc.Digest.String()))
			Expect(drainEvents()).To(BeEmpty())
		})

		It("keeps what it cached across inspectors of one directory, as across a container restart", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			cache := newCache()
			inspected(newTester(reg.Client(), nil, cache), packageAt(reg, "pkg", ""))
			reg.ResetRequests()

			restarted := newTester(reg.Client(), nil, cache)
			Expect(resolveOf(restarted, packageAt(reg, "pkg", ""))).To(Equal(packageArtifactOutcome{
				Kind: packageArtifactAccepted, Pinned: pinnedAt(reg, "pkg", desc), Uninstall: artifact.UninstallSupported,
				RetryAt: fakeClock.Now().Add(packageArtifactResolvedFor),
			}))
			Expect(requests(reg, http.MethodGet, "manifests")).To(BeEmpty())
		})

		It("inspects a digest again whose cache entry is corrupt", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			dir := GinkgoT().TempDir()
			cache, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(dir, "sha256", desc.Digest.Encoded()+".json"), []byte("not json"), 0o600)).To(Succeed())
			t := newTester(reg.Client(), nil, cache)

			Expect(inspected(t, packageAt(reg, "pkg", "")).Kind).To(Equal(packageArtifactAccepted))
			_, ok, err := cache.get(desc.Digest.String())
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
		})
	})

	Describe("transient failures", func() {
		It("cost one timed-out connection per window for a black-holed host, whatever number of references it serves, and wait the package out", func() {
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
			// The client's own timeout is longer than the resolve and request timeouts, so the
			// context deadline decides.
			t := newTester(&http.Client{Transport: reachable.Client().Transport, Timeout: time.Minute}, nil, newCache())
			t.i.resolveTimeout = 100 * time.Millisecond
			t.i.requestTimeout = 100 * time.Millisecond

			host := listener.Addr().String()
			blackHoled := []*v1alpha1.Package{
				{PackageRef: v1alpha1.PackageRef{Name: "a", Version: version}, Image: host + "/a"},
				{PackageRef: v1alpha1.PackageRef{Name: "b", Version: version}, Image: host + "/b"},
				{PackageRef: v1alpha1.PackageRef{Name: "c", Version: version}, Image: host + "/c"},
				{PackageRef: v1alpha1.PackageRef{Name: "d", Version: version}, Image: host + "/d", ContainerSHA: "sha256:" + strings.Repeat("ab", 32)},
			}
			resolveAll := func() {
				for range 3 {
					for _, _package := range blackHoled {
						outcome := inspected(t, _package)
						Expect(outcome.Kind).To(Equal(packageArtifactUnavailable), _package.Name)
						Expect(outcome.ErrorClass).To(Equal("timeout"), _package.Name)
						Expect(outcome.RetryAt).To(BeTemporally(">", fakeClock.Now()), _package.Name)
						Expect(packageArtifactBlocks(outcome)).To(BeFalse(), _package.Name)
					}
				}
			}

			start := time.Now()
			resolveAll()
			// Within the resolve timeout, not the client's minute.
			Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
			Eventually(accepted.Load).Should(Equal(int64(1)))
			Consistently(accepted.Load, 300*time.Millisecond, 20*time.Millisecond).Should(Equal(int64(1)))
			warnings := warningsOf(drainEvents(), EventsReasonPackageArtifactInspectionFailed)
			Expect(warnings).To(HaveLen(len(blackHoled)))
			for _, warning := range warnings {
				Expect(warning).To(ContainSubstring("waits"))
			}

			// The window is per host: another registry is still asked in it.
			Expect(inspected(t, packageAt(reachable, "pkg", "")).Kind).To(Equal(packageArtifactAccepted))

			stepPast(packageArtifactUnavailableWait)
			resolveAll()
			Eventually(accepted.Load).Should(Equal(int64(2)))
			Consistently(accepted.Load, 300*time.Millisecond, 20*time.Millisecond).Should(Equal(int64(2)))
			// Still the same outage, so no Warning again.
			Expect(warningsOf(drainEvents(), EventsReasonPackageArtifactInspectionFailed)).To(BeEmpty())
		})

		// A host that answers a status was connected to, so the failure is the reference's own:
		// another repository on the host may well answer.
		DescribeTable("cost each reference on a host answering a status one request per window, and none in it",
			func(status int) {
				var (
					mu     sync.Mutex
					served = map[string]int{}
				)
				failing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					repository, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")
					mu.Lock()
					served[repository]++
					mu.Unlock()
					w.WriteHeader(status)
				}))
				DeferCleanup(failing.Close)
				total := func() int {
					mu.Lock()
					defer mu.Unlock()
					n := 0
					for _, count := range served {
						n += count
					}
					return n
				}
				// It never retries, and trusts the server.
				t := newTester(failing.Client(), nil, newCache())

				host := failing.Listener.Addr().String()
				refs := []*v1alpha1.Package{
					{PackageRef: v1alpha1.PackageRef{Name: "a", Version: version}, Image: host + "/a"},
					{PackageRef: v1alpha1.PackageRef{Name: "b", Version: version}, Image: host + "/b"},
					{PackageRef: v1alpha1.PackageRef{Name: "c", Version: version}, Image: host + "/c", ContainerSHA: "sha256:" + strings.Repeat("ab", 32)},
				}
				resolveAll := func() {
					for range 3 {
						for _, _package := range refs {
							outcome := inspected(t, _package)
							Expect(outcome.Kind).To(Equal(packageArtifactUnavailable), _package.Image)
							Expect(outcome.ErrorClass).To(Equal("unavailable"), _package.Image)
						}
					}
				}

				resolveAll()
				mu.Lock()
				Expect(served).To(HaveKey("a"))
				Expect(served).To(HaveKey("b"))
				Expect(served).To(HaveKey("c"))
				mu.Unlock()
				once := total()
				resolveAll()
				Expect(total()).To(Equal(once))

				stepPast(packageArtifactUnavailableWait)
				resolveAll()
				Expect(total()).To(Equal(2 * once))
			},
			Entry("a server error", http.StatusServiceUnavailable),
			Entry("too many requests, with no Retry-After", http.StatusTooManyRequests),
			Entry("a request timeout", http.StatusRequestTimeout),
		)

		It("read a healthy repository on a host whose other repositories keep failing, in the same pass and at every later window", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "healthy", version, registrytest.PackageSpec{})
			var healthyAsked atomic.Int64
			front := frontOf(reg, func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case strings.HasPrefix(r.URL.Path, "/v2/broken/"):
					w.WriteHeader(http.StatusBadGateway)
					return true
				case strings.HasPrefix(r.URL.Path, "/v2/hanging/"):
					// Past the client's timeout, as a proxy repository whose upstream is down.
					<-r.Context().Done()
					return true
				case strings.HasPrefix(r.URL.Path, "/v2/healthy/"):
					healthyAsked.Add(1)
				}
				return false
			}, nil)
			t := newTester(front.Client(), nil, newCache())
			t.i.resolveTimeout = 100 * time.Millisecond
			t.i.requestTimeout = 100 * time.Millisecond
			host := front.Listener.Addr().String()
			named := func(name string) *v1alpha1.Package {
				return &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: name, Version: version}, Image: host + "/" + name}
			}

			// In the order a pass asks them: by key, so the failing ones first.
			for window := range 4 {
				if window > 0 {
					stepPast(packageArtifactUnavailableWait)
				}
				asked := healthyAsked.Load()
				broken := resolveOf(t, named("broken"))
				hanging := resolveOf(t, named("hanging"))
				healthy := inspected(t, named("healthy"))
				Expect(healthy.Kind).To(Equal(packageArtifactAccepted), "window %d", window)
				Expect(healthyAsked.Load()).To(BeNumerically(">", asked), "window %d", window)
				Expect(broken.Kind).To(Equal(packageArtifactUnavailable), "window %d", window)
				Expect(broken.ErrorClass).To(Equal("unavailable"), "window %d", window)
				Expect(hanging.Kind).To(Equal(packageArtifactUnavailable), "window %d", window)
				Expect(hanging.ErrorClass).To(Equal("timeout"), "window %d", window)
			}
		})

		It("never extend the window of a host by a reference asked for in it, so the host is asked again when its first failure is due", func() {
			var (
				refusing atomic.Bool
				refused  atomic.Int64
			)
			refusing.Store(true)
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "a", version, registrytest.PackageSpec{})
			pushPackage(reg, "b", version, registrytest.PackageSpec{})
			// A host that cannot be connected to until it stops refusing, then a proxy to reg, so the
			// host is the same throughout.
			front := frontOf(reg, nil, func(l net.Listener) net.Listener {
				return refusingListener{Listener: l, refusing: &refusing, refused: &refused}
			})
			t := newTester(front.Client(), nil, newCache())
			host := front.Listener.Addr().String()
			a := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "a", Version: version}, Image: host + "/a"}
			b := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "b", Version: version}, Image: host + "/b"}

			first := resolveOf(t, a)
			Expect(first.Kind).To(Equal(packageArtifactUnavailable))
			Expect(refused.Load()).To(BeNumerically(">", 0))

			// b is first asked for inside a's window: no connection, and no later than a's window.
			fakeClock.SetTime(fakeClock.Now().Add(packageArtifactUnavailableWait / 3))
			asked := refused.Load()
			second := resolveOf(t, b)
			Expect(second.Kind).To(Equal(packageArtifactUnavailable))
			Expect(refused.Load()).To(Equal(asked))
			Expect(second.RetryAt).To(Equal(first.RetryAt))

			// The host can be connected to by the time a is due: a is asked, and read.
			refusing.Store(false)
			fakeClock.SetTime(first.RetryAt)
			Expect(inspected(t, a).Kind).To(Equal(packageArtifactAccepted))
			Expect(inspected(t, b).Kind).To(Equal(packageArtifactAccepted))
		})

		It("are never retried by the registry client, so a host failing every repository costs one round trip for each", func() {
			var served atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				served.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			DeferCleanup(server.Close)
			inspector := newPackageArtifactInspector(nil, recorder, namespace, nil, newCache())

			resp, err := inspector.httpClient.Get(server.URL + "/v2/")
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Body.Close()).To(Succeed())
			Expect(resp.StatusCode).To(Equal(http.StatusServiceUnavailable))
			Expect(served.Load()).To(Equal(int64(1)))
		})

		It("leave a host open that refused a credential or has no such tag", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			secret := pullSecretNamed(pullSecret, dockerConfigJSON(reg, "not-the-password"))
			t := newTester(reg.Client(), []string{pullSecret}, newCache(), secret)
			missing := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "missing", Version: version}, Image: reg.Host() + "/missing"}

			Expect(resolveOf(t, packageAt(reg, "pkg", "")).ErrorClass).To(Equal("unauthorized"))
			Expect(t.c.Get(ctx, client.ObjectKeyFromObject(secret), secret)).To(Succeed())
			secret.Data[corev1.DockerConfigJsonKey] = dockerConfigJSON(reg, password)
			Expect(t.c.Update(ctx, secret)).To(Succeed())

			reg.ResetRequests()
			Expect(resolveOf(t, missing).ErrorClass).To(Equal("not-found"))
			Expect(reg.Requests()).NotTo(BeEmpty())
			reg.ResetRequests()
			Expect(resolveOf(t, packageAt(reg, "other", "")).ErrorClass).To(Equal("not-found"))
			Expect(reg.Requests()).NotTo(BeEmpty())
		})
	})

	Describe("credentials", func() {
		It("authenticate with a dockerconfigjson Secret, to resolve and to inspect", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), []string{pullSecret}, newCache(), pullSecretNamed(pullSecret, dockerConfigJSON(reg, password)))

			Expect(inspected(t, packageAt(reg, "pkg", "")).Kind).To(Equal(packageArtifactAccepted))
			Expect(drainEvents()).To(HaveLen(1)) // UninstallCapability alone
		})

		It("are anonymous with no Secret configured", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), nil, newCache())

			Expect(resolveOf(t, packageAt(reg, "pkg", ""))).To(HaveField("ErrorClass", "unauthorized"))
		})

		It("try the Secrets in order, past one whose credential the registry refuses, and are read again at every resolution of the tag", func() {
			reg := newRegistry(registrytest.Options{Username: username, Password: password})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			wrong := pullSecretNamed("wrong", dockerConfigJSON(reg, "not-the-password"))
			right := pullSecretNamed(pullSecret, dockerConfigJSON(reg, password))
			t := newTester(reg.Client(), []string{"wrong", pullSecret}, newCache(), wrong, right)
			_package := packageAt(reg, "pkg", "")

			Expect(inspected(t, _package).Kind).To(Equal(packageArtifactAccepted))

			// Rotated to a refused credential, the next resolution of the tag reads it.
			stale := right.DeepCopy()
			Expect(t.c.Get(ctx, client.ObjectKeyFromObject(right), stale)).To(Succeed())
			stale.Data[corev1.DockerConfigJsonKey] = dockerConfigJSON(reg, "rotated-away")
			Expect(t.c.Update(ctx, stale)).To(Succeed())
			stepPast(packageArtifactResolvedFor)
			Expect(resolveOf(t, _package)).To(HaveField("ErrorClass", "unauthorized"))
		})

		// Each unusable Secret carries the registry's credentials where it carries any, so only
		// a resolution that passed over it answers unauthorized.
		DescribeTable("record one Warning per unusable Secret, named in its action, until it can be read, and go on with the rest",
			func(secret func(name string, dockerConfig []byte) *corev1.Secret, action string) {
				reg := newRegistry(registrytest.Options{Username: username, Password: password})
				pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
				objects := []client.Object{pullSecretNamed("later", dockerConfigJSON(reg, password))}
				if s := secret(pullSecret, dockerConfigJSON(reg, password)); s != nil {
					objects = append(objects, s)
				}
				t := newTester(reg.Client(), []string{pullSecret, "later"}, newCache(), objects...)
				_package := packageAt(reg, "pkg", "")

				Expect(inspected(t, _package).Kind).To(Equal(packageArtifactAccepted))
				resolveOf(t, _package)
				warnings := warningsOf(drainEvents(), EventsReasonPackageArtifactPullSecret)
				Expect(warnings).To(HaveLen(1))
				Expect(warnings[0]).To(HavePrefix("Warning PackageArtifactPullSecret " + action + ":" + pullSecret + " "))
				Expect(warnings[0]).To(ContainSubstring(namespace + "/" + pullSecret))
				Expect(warnings[0]).NotTo(ContainSubstring(password))

				// Once the Secret is usable, a later problem with it is reported again.
				usable := pullSecretNamed(pullSecret, dockerConfigJSON(reg, password))
				existing := &corev1.Secret{}
				if err := t.c.Get(ctx, client.ObjectKeyFromObject(usable), existing); err == nil {
					Expect(t.c.Delete(ctx, existing)).To(Succeed())
				}
				Expect(t.c.Create(ctx, usable)).To(Succeed())
				stepPast(packageArtifactResolvedFor)
				resolveOf(t, _package)
				Expect(warningsOf(drainEvents(), EventsReasonPackageArtifactPullSecret)).To(BeEmpty())
				Expect(t.c.Delete(ctx, usable)).To(Succeed())
				stepPast(packageArtifactResolvedFor)
				resolveOf(t, _package)
				Expect(warningsOf(drainEvents(), EventsReasonPackageArtifactPullSecret)).To(HaveLen(1))
			},
			Entry("a missing Secret", func(string, []byte) *corev1.Secret { return nil }, "SecretUnreadable"),
			Entry("an Opaque Secret", func(name string, dockerConfig []byte) *corev1.Secret {
				return &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
					Type:       corev1.SecretTypeOpaque,
					Data:       map[string][]byte{corev1.DockerConfigJsonKey: dockerConfig},
				}
			}, "SecretWrongType"),
			Entry("a dockerconfigjson Secret without its key", func(name string, dockerConfig []byte) *corev1.Secret {
				return &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
					Type:       corev1.SecretTypeDockerConfigJson,
					Data:       map[string][]byte{corev1.DockerConfigKey: dockerConfig},
				}
			}, "SecretMissingKey"),
		)
	})

	Describe("the inspector's workers", func() {
		It("queue a digest whose result could not be cached again", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			dir := GinkgoT().TempDir()
			cache, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			t := newTester(reg.Client(), nil, cache)
			_package := packageAt(reg, "pkg", "")
			pending := resolveOf(t, _package)
			Expect(pending.Kind).To(Equal(packageArtifactPending))
			files := filepath.Join(dir, "sha256")
			Expect(os.Chmod(files, 0o500)).To(Succeed())
			DeferCleanup(os.Chmod, files, os.FileMode(0o700))

			Expect(t.i.processNext(ctx)).To(BeTrue())
			Expect(t.i.queue.NumRequeues(packageArtifactRequest{nodewright: nwName, name: "pkg", version: version, pinned: pending.Pinned})).To(Equal(1))

			Expect(os.Chmod(files, 0o700)).To(Succeed())
			Expect(t.i.processNext(ctx)).To(BeTrue())
			Expect(resolveOf(t, _package).Kind).To(Equal(packageArtifactAccepted))
		})

		It("inspect once a digest two passes ask for before it is inspected, and never one already cached", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), nil, newCache())
			_package := packageAt(reg, "pkg", "")

			resolveOf(t, _package)
			pending := resolveOf(t, _package)
			Expect(t.i.queue.Len()).To(Equal(1))
			reg.ResetRequests()
			t.i.drain(ctx)
			Expect(requests(reg, http.MethodGet, "manifests")).To(HaveLen(1))

			reg.ResetRequests()
			t.i.want(packageArtifactRequest{nodewright: nwName, name: "pkg", version: version, pinned: pending.Pinned})
			t.i.drain(ctx)
			Expect(reg.Requests()).To(BeEmpty())
		})

		It("run until the context is done, caching what is asked meanwhile", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "pkg", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), nil, newCache())
			Expect(t.i.NeedLeaderElection()).To(BeTrue())
			running, stop := context.WithCancel(ctx)
			done := make(chan error)
			go func() { done <- t.i.Start(running) }()

			_package := packageAt(reg, "pkg", "")
			Expect(resolveOf(t, _package).Kind).To(Equal(packageArtifactPending))
			Eventually(func() packageArtifactKind { return resolveOf(t, _package).Kind }).Should(Equal(packageArtifactAccepted))

			stop()
			Eventually(done).Should(Receive(BeNil()))
		})
	})

	Describe("pruning", func() {
		// named is the package name at the repository of that name on reg, by its tag, or by
		// containerSHA when that is set.
		named := func(reg *registrytest.Registry, name, containerSHA string) *v1alpha1.Package {
			return &v1alpha1.Package{
				PackageRef:   v1alpha1.PackageRef{Name: name, Version: version},
				Image:        reg.Host() + "/" + name,
				ContainerSHA: containerSHA,
			}
		}

		// naming is the NodeWright name whose spec holds packages.
		naming := func(name string, packages ...*v1alpha1.Package) v1alpha1.NodeWright {
			nw := v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}}}
			for _, _package := range packages {
				nw.Spec.Packages[_package.Name] = *_package
			}
			return nw
		}

		cached := func(cache *packageArtifactCache, desc ocispec.Descriptor) bool {
			GinkgoHelper()
			_, ok, err := cache.get(desc.Digest.String())
			Expect(err).NotTo(HaveOccurred())
			return ok
		}

		// pushNamed pushes a package to the repository name whose payload is its name, so that no two
		// repositories hold one digest.
		pushNamed := func(reg *registrytest.Registry, name string) ocispec.Descriptor {
			return pushPackage(reg, name, version, registrytest.PackageSpec{Payload: []registrytest.Layer{{Gzip: []byte(name)}}})
		}

		referenceOf := func(_package *v1alpha1.Package) string {
			GinkgoHelper()
			ref, err := packageReference(_package)
			Expect(err).NotTo(HaveOccurred())
			return ref.String()
		}

		It("keeps the entries a NodeWright names, by containerSHA or by the last resolution of a tag whatever its age, and removes every other", func() {
			reg := newRegistry(registrytest.Options{})
			tagged := pushNamed(reg, "tagged")
			pinned := pushNamed(reg, "pinned")
			other := pushNamed(reg, "other")
			cache := newCache()
			t := newTester(reg.Client(), nil, cache)
			byTag, byDigest, unnamed := named(reg, "tagged", ""), named(reg, "pinned", pinned.Digest.String()), named(reg, "other", "")
			for _, _package := range []*v1alpha1.Package{byTag, byDigest, unnamed} {
				Expect(inspected(t, _package).Kind).To(Equal(packageArtifactAccepted), _package.Name)
			}
			// The file of a write the process never finished, as when it was killed between the
			// create and the rename: pruning holds the lock of a write, so no write is in flight.
			leftover := filepath.Join(cache.dir, "sha256", ".entry-leftover")
			Expect(os.WriteFile(leftover, []byte("{}"), 0o600)).To(Succeed())
			stepPast(packageArtifactResolvedFor)

			reg.ResetRequests()
			t.i.prune(ctx, []v1alpha1.NodeWright{naming("a", byTag), naming("b", byDigest)})

			Expect(reg.Requests()).To(BeEmpty())
			Expect(cached(cache, tagged)).To(BeTrue())
			Expect(cached(cache, pinned)).To(BeTrue())
			Expect(cached(cache, other)).To(BeFalse())
			Expect(leftover).NotTo(BeAnExistingFile())
		})

		It("removes the digest a re-pushed tag named once the tag resolves to another", func() {
			reg := newRegistry(registrytest.Options{})
			first := pushNamed(reg, "tagged")
			cache := newCache()
			t := newTester(reg.Client(), nil, cache)
			byTag := named(reg, "tagged", "")
			nodewrights := []v1alpha1.NodeWright{naming("a", byTag)}
			Expect(inspected(t, byTag).Kind).To(Equal(packageArtifactAccepted))
			second := pushPackage(reg, "tagged", version, registrytest.PackageSpec{Uninstall: artifact.UninstallUnsupported})
			Expect(second.Digest).NotTo(Equal(first.Digest))

			// Until the tag resolves again, its last resolution is what the NodeWright names.
			t.i.prune(ctx, nodewrights)
			Expect(cached(cache, first)).To(BeTrue())

			stepPast(packageArtifactResolvedFor)
			Expect(inspected(t, byTag).Pinned).To(Equal(pinnedAt(reg, "tagged", second)))
			t.i.prune(ctx, nodewrights)
			Expect(cached(cache, first)).To(BeFalse())
			Expect(cached(cache, second)).To(BeTrue())
		})

		It("removes the entries of a package its NodeWright no longer holds, and of a NodeWright that is gone", func() {
			reg := newRegistry(registrytest.Options{})
			kept := pushNamed(reg, "kept")
			dropped := pushNamed(reg, "dropped")
			cache := newCache()
			t := newTester(reg.Client(), nil, cache)
			keep, drop := named(reg, "kept", ""), named(reg, "dropped", "")
			Expect(inspected(t, keep).Kind).To(Equal(packageArtifactAccepted))
			Expect(inspected(t, drop).Kind).To(Equal(packageArtifactAccepted))

			t.i.prune(ctx, []v1alpha1.NodeWright{naming("a", keep, drop)})
			Expect(cached(cache, kept)).To(BeTrue())
			Expect(cached(cache, dropped)).To(BeTrue())

			t.i.prune(ctx, []v1alpha1.NodeWright{naming("a", keep)})
			Expect(cached(cache, kept)).To(BeTrue())
			Expect(cached(cache, dropped)).To(BeFalse())

			t.i.prune(ctx, nil)
			Expect(cached(cache, kept)).To(BeFalse())
		})

		It("keeps after a restart the entries of the repository of a tag not resolved since, and removes those of a repository no NodeWright names", func() {
			reg := newRegistry(registrytest.Options{})
			tagged := pushNamed(reg, "tagged")
			other := pushNamed(reg, "other")
			dir := GinkgoT().TempDir()
			before, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			byTag, unnamed := named(reg, "tagged", ""), named(reg, "other", "")
			t := newTester(reg.Client(), nil, before)
			Expect(inspected(t, byTag).Kind).To(Equal(packageArtifactAccepted))
			Expect(inspected(t, unnamed).Kind).To(Equal(packageArtifactAccepted))

			// A container restart keeps the directory and loses the inspector's memory.
			after, err := newPackageArtifactCache(dir)
			Expect(err).NotTo(HaveOccurred())
			restarted := newTester(reg.Client(), nil, after)
			reg.ResetRequests()
			restarted.i.prune(ctx, []v1alpha1.NodeWright{naming("a", byTag)})
			Expect(reg.Requests()).To(BeEmpty())
			Expect(cached(after, tagged)).To(BeTrue())
			Expect(cached(after, other)).To(BeFalse())

			// The entry kept answers once the tag resolves: no inspection.
			Expect(inspected(restarted, byTag).Kind).To(Equal(packageArtifactAccepted))
			Expect(requests(reg, http.MethodGet, "manifests")).To(BeEmpty())
		})

		It("forgets the references no NodeWright names", func() {
			reg := newRegistry(registrytest.Options{})
			pushNamed(reg, "tagged")
			pushNamed(reg, "other")
			t := newTester(reg.Client(), nil, newCache())
			byTag, unnamed, missing := named(reg, "tagged", ""), named(reg, "other", ""), named(reg, "missing", "")
			Expect(inspected(t, byTag).Kind).To(Equal(packageArtifactAccepted))
			Expect(inspected(t, unnamed).Kind).To(Equal(packageArtifactAccepted))
			Expect(resolveOf(t, missing).ErrorClass).To(Equal("not-found"))

			t.i.prune(ctx, []v1alpha1.NodeWright{naming("a", byTag, missing)})
			Expect(t.i.resolved).To(HaveKey(referenceOf(byTag)))
			Expect(t.i.resolved).NotTo(HaveKey(referenceOf(unnamed)))
			Expect(t.i.failures).To(HaveKey(referenceOf(missing)))

			t.i.prune(ctx, []v1alpha1.NodeWright{naming("a", byTag)})
			Expect(t.i.resolved).To(HaveKey(referenceOf(byTag)))
			Expect(t.i.failures).NotTo(HaveKey(referenceOf(missing)))
		})
	})

	Describe("resolvePackageArtifacts", func() {
		pkgA := &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: "a", Version: version}}

		// reconciler is a reconciler with the option as on says, whose inspector is t's, over the
		// objects t's client holds.
		reconciler := func(t tester, on bool) *SkyhookReconciler {
			GinkgoHelper()
			opts := validOpts()
			opts.PackageArtifacts = on
			opts.artifactInspector = t.i
			r, err := NewSkyhookReconciler(newScheme(), t.c, t.c, k8sfake.NewClientset(), recorder, opts)
			Expect(err).NotTo(HaveOccurred())
			return r
		}

		// stateOf is the SkyhookNodes of nw on nodes, as a pass builds it.
		stateOf := func(nw *v1alpha1.NodeWright, nodes ...*corev1.Node) SkyhookNodes {
			GinkgoHelper()
			list := &corev1.NodeList{}
			for _, node := range nodes {
				list.Items = append(list.Items, *node)
			}
			state, err := BuildState(&v1alpha1.NodeWrightList{Items: []v1alpha1.NodeWright{*nw}}, list, &v1alpha1.DeploymentPolicyList{})
			Expect(err).NotTo(HaveOccurred())
			return state.skyhooks[0]
		}

		nodeAt := func(name string, _package *v1alpha1.Package, stage v1alpha1.Stage, state v1alpha1.State) *corev1.Node {
			GinkgoHelper()
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
			if _package != nil {
				sn, err := wrapper.NewSkyhookNodeOnly(node, nwName)
				Expect(err).NotTo(HaveOccurred())
				Expect(sn.Upsert(_package.PackageRef, _package.Image, state, stage, 0, _package.ContainerSHA)).To(Succeed())
			}
			return node
		}

		nodeWrightOf := func(packages ...*v1alpha1.Package) *v1alpha1.NodeWright {
			nw := &v1alpha1.NodeWright{ObjectMeta: metav1.ObjectMeta{Name: nwName}, Spec: v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}}}
			for _, _package := range packages {
				nw.Spec.Packages[_package.Name] = *_package
			}
			return nw
		}

		It("resolves the next package of every node once per reference, and gives every node what it resolved", func() {
			reg := newRegistry(registrytest.Options{})
			desc := pushPackage(reg, "a", version, registrytest.PackageSpec{})
			t := newTester(reg.Client(), nil, newCache())
			_package := pkgA.DeepCopy()
			_package.Image = reg.Host() + "/a"
			skyhook := stateOf(nodeWrightOf(_package), nodeAt("n1", nil, "", ""), nodeAt("n2", nil, "", ""))
			reg.ResetRequests()

			result, err := reconciler(t, true).resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(&ctrl.Result{RequeueAfter: packageArtifactPendingRequeue}))
			Expect(requests(reg, http.MethodHead, "manifests")).To(HaveLen(1))
			want := map[string]wrapper.PackageArtifact{packageArtifactKey(_package): {Kind: "pending", Pinned: pinnedAt(reg, "a", desc)}}
			Expect(skyhook.GetSkyhook().PackageArtifacts).To(Equal(want))
			for _, node := range skyhook.GetNodes() {
				Expect(node.GetSkyhook().PackageArtifacts).To(Equal(want), node.GetNode().Name)
			}

			t.i.drain(ctx)
			skyhook = stateOf(nodeWrightOf(_package), nodeAt("n1", nil, "", ""))
			result, err = reconciler(t, true).resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			// Once the digest stops answering for the tag.
			Expect(result).To(Equal(&ctrl.Result{RequeueAfter: packageArtifactResolvedFor}))
			Expect(skyhook.GetSkyhook().PackageArtifacts).To(HaveKeyWithValue(packageArtifactKey(_package), HaveField("Kind", "artifact")))
		})

		It("resolves nothing for a package whose Job runs, or at an interrupt stage, next or not yet complete", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "a", version, registrytest.PackageSpec{})
			_package := pkgA.DeepCopy()
			_package.Image = reg.Host() + "/a"
			_package.Interrupt = &v1alpha1.Interrupt{Type: v1alpha1.REBOOT}
			opts := validOpts()
			running := createJobFromPackage(opts, _package, wrapper.NewSkyhookWrapper(nodeWrightOf(_package)), "n1", v1alpha1.StageApply)
			running.Namespace = namespace
			t := newTester(reg.Client(), nil, newCache(), running)
			skyhook := stateOf(nodeWrightOf(_package),
				nodeAt("n1", _package, v1alpha1.StageApply, v1alpha1.StateInProgress),
				nodeAt("n2", _package, v1alpha1.StageConfig, v1alpha1.StateComplete),
				nodeAt("n3", _package, v1alpha1.StageInterrupt, v1alpha1.StateInProgress),
				nodeAt("n4", _package, v1alpha1.StageInterrupt, v1alpha1.StateErroring))
			reg.ResetRequests()

			result, err := reconciler(t, true).resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
			Expect(reg.Requests()).To(BeEmpty())
			Expect(skyhook.GetSkyhook().PackageArtifacts).To(BeEmpty())
		})

		It("lists the Jobs of the NodeWright once in a pass, whatever number of nodes it decides for", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "a", version, registrytest.PackageSpec{})
			_package := pkgA.DeepCopy()
			_package.Image = reg.Host() + "/a"
			running := createJobFromPackage(validOpts(), _package, wrapper.NewSkyhookWrapper(nodeWrightOf(_package)), "n1", v1alpha1.StageApply)
			running.Namespace = namespace
			t := newTester(reg.Client(), nil, newCache(), running)
			var lists atomic.Int64
			watched, ok := t.c.(client.WithWatch)
			Expect(ok).To(BeTrue())
			t.c = interceptor.NewClient(watched, interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, jobs := list.(*batchv1.JobList); jobs {
						lists.Add(1)
					}
					return c.List(ctx, list, opts...)
				},
			})
			skyhook := stateOf(nodeWrightOf(_package),
				nodeAt("n1", _package, v1alpha1.StageApply, v1alpha1.StateInProgress),
				nodeAt("n2", nil, "", ""), nodeAt("n3", nil, "", ""), nodeAt("n4", nil, "", ""))

			_, err := reconciler(t, true).resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			Expect(lists.Load()).To(Equal(int64(1)))
			Expect(skyhook.GetSkyhook().PackageArtifacts).To(HaveKey(packageArtifactKey(_package)))
		})

		It("resolves a package whose uninstall is decided, on a node where it is complete", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "a", version, registrytest.PackageSpec{})
			_package := pkgA.DeepCopy()
			_package.Image = reg.Host() + "/a"
			_package.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
			t := newTester(reg.Client(), nil, newCache())
			skyhook := stateOf(nodeWrightOf(_package), nodeAt("n1", _package, v1alpha1.StageConfig, v1alpha1.StateComplete))

			_, err := reconciler(t, true).resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			Expect(skyhook.GetSkyhook().PackageArtifacts).To(HaveKey(packageArtifactKey(_package)))
		})

		It("asks for the pass again once a reference that could not be read is due, and names the nodes a blocked package leaves blocked", func() {
			reg := newRegistry(registrytest.Options{})
			missing := pkgA.DeepCopy()
			missing.Image = reg.Host() + "/missing"
			t := newTester(reg.Client(), nil, newCache())
			skyhook := stateOf(nodeWrightOf(missing), nodeAt("n1", nil, "", ""), nodeAt("n2", missing, v1alpha1.StageConfig, v1alpha1.StateComplete))

			result, err := reconciler(t, true).resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(&ctrl.Result{RequeueAfter: packageArtifactRetry}))
			Expect(skyhook.GetSkyhook().PackageArtifactBlockedNodes).To(Equal(map[string]bool{"n1": true}))
		})

		It("asks for the pass again after packageArtifactUnavailableWait for a reference that is unavailable, and blocks no node", func() {
			failing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			DeferCleanup(failing.Close)
			unavailable := pkgA.DeepCopy()
			unavailable.Image = failing.Listener.Addr().String() + "/a"
			t := newTester(failing.Client(), nil, newCache())
			skyhook := stateOf(nodeWrightOf(unavailable), nodeAt("n1", nil, "", ""))

			result, err := reconciler(t, true).resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(&ctrl.Result{RequeueAfter: packageArtifactUnavailableWait}))
			Expect(skyhook.GetSkyhook().PackageArtifactBlockedNodes).To(BeEmpty())
		})

		// statesOf is the SkyhookNodes of each of nws on nodes, as a pass builds them.
		statesOf := func(nws []*v1alpha1.NodeWright, nodes ...*corev1.Node) []SkyhookNodes {
			GinkgoHelper()
			list := &corev1.NodeList{}
			for _, node := range nodes {
				list.Items = append(list.Items, *node)
			}
			nwList := &v1alpha1.NodeWrightList{}
			for _, nw := range nws {
				nwList.Items = append(nwList.Items, *nw)
			}
			state, err := BuildState(nwList, list, &v1alpha1.DeploymentPolicyList{})
			Expect(err).NotTo(HaveOccurred())
			return state.skyhooks
		}

		It("waits on a host whose repositories accept the request and never answer for its probe and then the rest at once, whatever number of NodeWrights name them", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "healthy", version, registrytest.PackageSpec{})
			front := frontOf(reg, func(_ http.ResponseWriter, r *http.Request) bool {
				if strings.HasPrefix(r.URL.Path, "/v2/hanging-") {
					// Past the client's timeout, as a proxy repository whose upstream is down.
					<-r.Context().Done()
					return true
				}
				return false
			}, nil)
			const timeout = 300 * time.Millisecond
			t := newTester(front.Client(), nil, newCache())
			t.i.resolveTimeout = timeout
			host := front.Listener.Addr().String()
			named := func(name, repository string) *v1alpha1.Package {
				return &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: name, Version: version}, Image: host + "/" + repository}
			}
			hanging := []*v1alpha1.Package{
				named("a", "hanging-a"), named("b", "hanging-b"), named("c", "hanging-c"),
				named("d", "hanging-d"), named("e", "hanging-e"), named("f", "hanging-f"),
			}
			first := nodeWrightOf(append(slices.Clone(hanging[:3]), named("healthy", "healthy"))...)
			second := nodeWrightOf(append(slices.Clone(hanging[3:]), named("same", "healthy"))...)
			second.Name = "second"
			skyhooks := statesOf([]*v1alpha1.NodeWright{first, second}, nodeAt("n1", nil, "", ""))
			Expect(skyhooks).To(HaveLen(2))
			reg.ResetRequests()

			start := time.Now()
			_, err := reconciler(t, true).resolvePackageArtifactsOf(ctx, skyhooks)
			Expect(err).NotTo(HaveOccurred())
			// One after another, six references that never answer cost six timeouts; a probe and
			// then the rest at once, two.
			Expect(time.Since(start)).To(BeNumerically("<", 4*timeout))

			resolved := map[string]wrapper.PackageArtifact{}
			for _, skyhook := range skyhooks {
				maps.Copy(resolved, skyhook.GetSkyhook().PackageArtifacts)
			}
			for _, _package := range hanging {
				Expect(resolved).To(HaveKeyWithValue(packageArtifactKey(_package), And(
					HaveField("Kind", string(packageArtifactUnavailable)), HaveField("Reason", "timeout"))), _package.Name)
			}
			// The healthy repository of the host is read in the same pass, once for both NodeWrights.
			Expect(resolved).To(HaveKeyWithValue(packageArtifactKey(named("healthy", "healthy")), HaveField("Kind", string(packageArtifactPending))))
			Expect(resolved).To(HaveKeyWithValue(packageArtifactKey(named("same", "healthy")), HaveField("Kind", string(packageArtifactPending))))
			Expect(requests(reg, http.MethodHead, "manifests")).To(HaveLen(1))
		})

		It("waits on a host whose repositories accept the request and never answer for about three timeouts, whatever number of references it serves", func() {
			reg := newRegistry(registrytest.Options{})
			// Sorts first, so the probe answers and the host is resolved packageArtifactHostResolutions
			// at a time until one of them times out.
			pushPackage(reg, "a-healthy", version, registrytest.PackageSpec{})
			front := frontOf(reg, func(_ http.ResponseWriter, r *http.Request) bool {
				if strings.HasPrefix(r.URL.Path, "/v2/hanging-") {
					<-r.Context().Done()
					return true
				}
				return false
			}, nil)
			const timeout = 300 * time.Millisecond
			t := newTester(front.Client(), nil, newCache())
			t.i.resolveTimeout = timeout
			host := front.Listener.Addr().String()
			named := func(name, repository string) *v1alpha1.Package {
				return &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: name, Version: version}, Image: host + "/" + repository}
			}
			var hanging []*v1alpha1.Package
			for i := range 5 * packageArtifactHostResolutions {
				hanging = append(hanging, named(fmt.Sprintf("p%02d", i), fmt.Sprintf("hanging-%02d", i)))
			}
			half := len(hanging) / 2
			first := nodeWrightOf(append(slices.Clone(hanging[:half]), named("healthy", "a-healthy"))...)
			second := nodeWrightOf(hanging[half:]...)
			second.Name = "second"
			skyhooks := statesOf([]*v1alpha1.NodeWright{first, second}, nodeAt("n1", nil, "", ""))
			Expect(skyhooks).To(HaveLen(2))

			start := time.Now()
			_, err := reconciler(t, true).resolvePackageArtifactsOf(ctx, skyhooks)
			Expect(err).NotTo(HaveOccurred())
			// packageArtifactHostResolutions at a time, forty references that never answer cost five
			// timeouts; once one of them has timed out the rest go at once, so three at most.
			Expect(time.Since(start)).To(BeNumerically("<", 4*timeout))

			resolved := map[string]wrapper.PackageArtifact{}
			for _, skyhook := range skyhooks {
				maps.Copy(resolved, skyhook.GetSkyhook().PackageArtifacts)
			}
			for _, _package := range hanging {
				Expect(resolved).To(HaveKeyWithValue(packageArtifactKey(_package), HaveField("Kind", string(packageArtifactUnavailable))), _package.Name)
			}
			Expect(resolved).To(HaveKeyWithValue(packageArtifactKey(named("healthy", "a-healthy")), HaveField("Kind", string(packageArtifactPending))))
		})

		It("connects once in a pass to a black-holed host, whatever number of references and NodeWrights it serves", func() {
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
			t := newTester(&http.Client{Timeout: time.Minute}, nil, newCache())
			t.i.resolveTimeout = 100 * time.Millisecond
			host := listener.Addr().String()
			named := func(name string) *v1alpha1.Package {
				return &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: name, Version: version}, Image: host + "/" + name}
			}
			second := nodeWrightOf(named("c"), named("d"))
			second.Name = "second"
			skyhooks := statesOf([]*v1alpha1.NodeWright{nodeWrightOf(named("a"), named("b")), second}, nodeAt("n1", nil, "", ""))

			for window := range 2 {
				if window > 0 {
					stepPast(packageArtifactUnavailableWait)
				}
				_, err = reconciler(t, true).resolvePackageArtifactsOf(ctx, skyhooks)
				Expect(err).NotTo(HaveOccurred())
				Eventually(accepted.Load).Should(Equal(int64(window + 1)))
				Consistently(accepted.Load, 300*time.Millisecond, 20*time.Millisecond).Should(Equal(int64(window + 1)))
				for _, skyhook := range skyhooks {
					for _, outcome := range skyhook.GetSkyhook().PackageArtifacts {
						Expect(outcome.Kind).To(Equal(string(packageArtifactUnavailable)), "window %d", window)
					}
				}
			}
		})

		It("asks for the pass again once the digest a tag resolved to stops answering for it, so a corrected push is picked up then", func() {
			reg := newRegistry(registrytest.Options{})
			hostFiles := []registrytest.HostFilesLayer{{Layer: registrytest.Layer{Gzip: []byte("host files")}, Destination: "/etc/nodewright"}}
			pushPackage(reg, "a", version, registrytest.PackageSpec{HostFiles: hostFiles})
			_package := pkgA.DeepCopy()
			_package.Image = reg.Host() + "/a"
			_package.Uninstall = &v1alpha1.Uninstall{Enabled: true}
			// Complete on its node and being deleted, so only its uninstall decision asks for a pass.
			nw := nodeWrightOf(_package)
			nw.DeletionTimestamp = ptr(metav1.Now())
			nw.Finalizers = []string{SkyhookFinalizer}
			t := newTester(reg.Client(), nil, newCache())
			r := reconciler(t, true)
			decide := func() (*ctrl.Result, wrapper.UninstallDecision) {
				GinkgoHelper()
				skyhook := stateOf(nw, nodeAt("n1", _package, v1alpha1.StageConfig, v1alpha1.StateComplete))
				result, err := r.resolvePackageArtifacts(ctx, skyhook)
				Expect(err).NotTo(HaveOccurred())
				r.resolveUninstallDecisions(ctx, skyhook)
				return result, skyhook.GetSkyhook().UninstallDecision(_package.Name)
			}

			decide()
			t.i.drain(ctx)
			result, decision := decide()
			Expect(decision).To(Equal(wrapper.UninstallDecisionDeferred))
			Expect(result).To(Equal(&ctrl.Result{RequeueAfter: packageArtifactResolvedFor}))

			pushPackage(reg, "a", version, registrytest.PackageSpec{})
			stepPast(packageArtifactResolvedFor)
			decide()
			t.i.drain(ctx)
			_, decision = decide()
			Expect(decision).To(Equal(wrapper.UninstallDecisionSpec))
		})

		It("resolves nothing with the option off, or for a paused NodeWright that is not being deleted, its uninstall decisions included", func() {
			reg := newRegistry(registrytest.Options{})
			pushPackage(reg, "a", version, registrytest.PackageSpec{})
			_package := pkgA.DeepCopy()
			_package.Image = reg.Host() + "/a"
			uninstalled := pkgA.DeepCopy()
			uninstalled.Name = "uninstalled"
			uninstalled.Image = reg.Host() + "/a"
			uninstalled.Uninstall = &v1alpha1.Uninstall{Enabled: true, Apply: true}
			t := newTester(reg.Client(), nil, newCache())
			reg.ResetRequests()

			skyhook := stateOf(nodeWrightOf(_package, uninstalled), nodeAt("n1", uninstalled, v1alpha1.StageConfig, v1alpha1.StateComplete))
			off := reconciler(t, false)
			result, err := off.resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
			off.resolveUninstallDecisions(ctx, skyhook)

			paused := nodeWrightOf(_package, uninstalled)
			paused.Annotations = map[string]string{v1alpha1.METADATA_PREFIX + "/pause": "true"}
			skyhook = stateOf(paused, nodeAt("n1", uninstalled, v1alpha1.StageConfig, v1alpha1.StateComplete))
			on := reconciler(t, true)
			result, err = on.resolvePackageArtifacts(ctx, skyhook)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
			on.resolveUninstallDecisions(ctx, skyhook)

			Expect(reg.Requests()).To(BeEmpty())
			Expect(t.i.queue.Len()).To(BeZero())
			Expect(skyhook.GetSkyhook().PackageArtifacts).To(BeEmpty())
		})
	})

	Describe("the Blocked condition of an unusable package artifact", func() {
		newReconciler := func(on bool) *SkyhookReconciler {
			GinkgoHelper()
			opts := validOpts()
			opts.PackageArtifacts = on
			opts.artifactInspector = newTester(nil, nil, newCache()).i
			c := fake.NewClientBuilder().Build()
			r, err := NewSkyhookReconciler(runtime.NewScheme(), c, c, k8sfake.NewClientset(), recorder, opts)
			Expect(err).NotTo(HaveOccurred())
			return r
		}

		// skyhookWith is a NodeWright of the packages outcomes names, which the pass resolved as
		// outcomes says.
		skyhookWith := func(outcomes map[string]packageArtifactOutcome) *wrapper.Skyhook {
			skyhook := wrapper.NewSkyhookWrapper(&v1alpha1.NodeWright{
				ObjectMeta: metav1.ObjectMeta{Name: nwName, Generation: 3},
				Spec:       v1alpha1.NodeWrightSpec{Packages: v1alpha1.Packages{}},
			})
			skyhook.PackageArtifacts = map[string]wrapper.PackageArtifact{}
			for name, outcome := range outcomes {
				_package := v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: name, Version: version}, Image: "registry.example.com/" + name}
				skyhook.Spec.Packages[name] = _package
				skyhook.PackageArtifacts[packageArtifactKey(&_package)] = outcome.memo()
			}
			return skyhook
		}

		blocked := func(skyhook *wrapper.Skyhook) *metav1.Condition {
			return meta.FindStatusCondition(skyhook.Status.Conditions, wrapper.SkyhookConditionBlocked)
		}

		It("lists every rejected or unreadable spec package, sorted, with why, and is removed once there is none", func() {
			skyhook := skyhookWith(map[string]packageArtifactOutcome{
				"c-unreadable": {Kind: packageArtifactUnreadable, ErrorClass: "unauthorized"},
				"a-rejected":   rejectedPackageArtifact("host-files"),
				"b-pending":    {Kind: packageArtifactPending},
				"d-waiting":    {Kind: packageArtifactUnavailable, ErrorClass: "timeout"},
				"e-image":      {Kind: packageArtifactImage},
			})
			r := newReconciler(true)
			r.updatePackageArtifactUnusableCondition(skyhook)
			condition := blocked(skyhook)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("PackageArtifactUnusable"))
			Expect(condition.ObservedGeneration).To(Equal(int64(3)))
			Expect(condition.Message).To(Equal("2 packages blocked: a-rejected:1.0.0 (host-files), c-unreadable:1.0.0 (unauthorized). " +
				"A package artifact in a private registry needs its pull Secret in imagePullSecrets."))

			skyhook.PackageArtifacts = map[string]wrapper.PackageArtifact{}
			r.updatePackageArtifactUnusableCondition(skyhook)
			Expect(blocked(skyhook)).To(BeNil())
		})

		It("lists ten and counts the rest", func() {
			outcomes := map[string]packageArtifactOutcome{}
			for i := range 11 {
				outcomes[fmt.Sprintf("p%02d", i)] = rejectedPackageArtifact("host-files")
			}
			skyhook := skyhookWith(outcomes)
			newReconciler(true).updatePackageArtifactUnusableCondition(skyhook)
			Expect(blocked(skyhook).Message).To(HavePrefix("11 packages blocked: p00:1.0.0 (host-files), "))
			Expect(blocked(skyhook).Message).To(ContainSubstring("p09:1.0.0 (host-files), and 1 more. "))
		})

		It("is removed with the option off", func() {
			skyhook := skyhookWith(map[string]packageArtifactOutcome{"a": rejectedPackageArtifact("host-files")})
			newReconciler(true).updatePackageArtifactUnusableCondition(skyhook)
			Expect(blocked(skyhook)).NotTo(BeNil())
			newReconciler(false).updatePackageArtifactUnusableCondition(skyhook)
			Expect(blocked(skyhook)).To(BeNil())
		})

		It("leaves a DependencyUninstalled Blocked as it is, and replaces a NonInterruptPodsRunning one", func() {
			for _, reason := range []string{"DependencyUninstalled", wrapper.SkyhookReasonNonInterruptPodsRunning} {
				skyhook := skyhookWith(map[string]packageArtifactOutcome{"a": rejectedPackageArtifact("host-files")})
				wrapper.AddSkyhookCondition(skyhook, metav1.Condition{
					Type: wrapper.SkyhookConditionBlocked, Status: metav1.ConditionTrue, Reason: reason, Message: "held", LastTransitionTime: metav1.Now(),
				})
				newReconciler(true).updatePackageArtifactUnusableCondition(skyhook)
				want := wrapper.SkyhookReasonPackageArtifactUnusable
				if reason == "DependencyUninstalled" {
					want = reason
				}
				Expect(blocked(skyhook).Reason).To(Equal(want), reason)
			}
		})
	})
})

// refusingListener closes each connection it accepts while refusing is set, before any TLS
// handshake, as a host that cannot be connected to, and counts them in refused.
type refusingListener struct {
	net.Listener
	refusing *atomic.Bool
	refused  *atomic.Int64
}

func (l refusingListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil || !l.refusing.Load() {
			return conn, err
		}
		l.refused.Add(1)
		_ = conn.Close()
	}
}
