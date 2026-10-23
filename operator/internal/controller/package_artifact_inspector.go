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

package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	artifactclient "github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	"oras.land/oras-go/v2/registry/remote/errcode"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// packageArtifactRequestTimeout bounds one inspection of one digest, every request of it
	// included.
	packageArtifactRequestTimeout = 10 * time.Second
	// packageArtifactResolveTimeout bounds the resolution of one tag in a pass, every request of it
	// included.
	packageArtifactResolveTimeout = 5 * time.Second
	// packageArtifactUnavailableWait is how long a reference that could not be read for a transient
	// reason is not asked again, and, when its registry host could not be connected to, that host.
	// Every reference on such a host asked for meanwhile is unavailable with no request, so a
	// black-holed registry costs one timeout per window, whatever number of references it serves. A
	// failure once connected, such as an error status or a repository that does not answer in time,
	// is the reference's own, so the other repositories of its host are still asked.
	packageArtifactUnavailableWait = 30 * time.Second
	// packageArtifactResolvedFor is how long the digest a tag resolved to answers for that tag. It
	// bounds a tag to one resolution per window, whatever number of packages, NodeWrights and passes
	// name it, so a re-pushed tag is picked up by a stage that starts that long after the push.
	packageArtifactResolvedFor = 30 * time.Second
	// packageArtifactInspectorWorkers is how many digests are inspected at once.
	packageArtifactInspectorWorkers = 4
)

// Reasons of a rejected outcome.
const (
	packageArtifactReasonMalformedManifest  = "malformed-manifest"
	packageArtifactReasonUnsupportedForm    = "unsupported-form"
	packageArtifactReasonUnsupportedVersion = "unsupported-version"
	// packageArtifactReasonInvalidArtifact is followed by the artifact.Reason of the rule broken.
	packageArtifactReasonInvalidArtifact = "invalid-artifact: "
	packageArtifactReasonHostFiles       = "host-files"
	packageArtifactReasonInterrupt       = "interrupt"
)

// Problems of the EventsReasonPackageArtifactPullSecret Warning, which open its action, followed by
// the Secret's name: the broadcaster folds Events with one action into one series and never sends a
// later note (see packageEventAction), so each problem of each Secret needs its own.
const (
	packageArtifactActionSecretUnreadable = "SecretUnreadable"
	packageArtifactActionSecretWrongType  = "SecretWrongType"
	packageArtifactActionSecretMissingKey = "SecretMissingKey"
)

// packageArtifactRequest asks for the digest pinned names, which a package version of one
// NodeWright resolved to, to be inspected and cached. It is comparable, so the queue holds one of
// each at a time.
type packageArtifactRequest struct {
	nodewright string
	name       string
	version    string
	pinned     string
}

func (q packageArtifactRequest) pkg() *v1alpha1.Package {
	return &v1alpha1.Package{PackageRef: v1alpha1.PackageRef{Name: q.name, Version: q.version}}
}

// packageArtifactFailure is the last failure to read one reference, a tag a pass resolves or a
// digest the inspector reads.
type packageArtifactFailure struct {
	class     string
	transient bool
	attempts  int32
	retryAt   time.Time
}

// packageArtifactResolution is the digest a tag resolved to, and when.
type packageArtifactResolution struct {
	digest string
	at     time.Time
}

// packageArtifactInspector reads package references at their registries for the operator. A pass
// resolves each reference it decides to the digest it names, with one manifest request, and finds
// what the digest holds in the cache (resolve); the inspector's workers inspect each digest the
// cache lacks, outside the pass, and cache what they find.
//
// It runs as a manager Runnable on the leader. It holds, besides the cache, only the digest each
// tag resolved to last, the last failure of each reference and the hosts that failed, which set
// when a reference is read again; losing them on a restart only reads it again sooner.
type packageArtifactInspector struct {
	client      client.Client
	recorder    events.EventRecorder
	namespace   string
	pullSecrets []string
	cache       *packageArtifactCache

	// Test seams. httpClient sends every registry request; a nil clock is the real clock; a zero
	// requestTimeout is packageArtifactRequestTimeout, and a zero resolveTimeout
	// packageArtifactResolveTimeout.
	httpClient     *http.Client
	clock          clock.PassiveClock
	requestTimeout time.Duration
	resolveTimeout time.Duration

	queue workqueue.TypedRateLimitingInterface[packageArtifactRequest]

	mu sync.Mutex
	// hosts holds, by registry API host, the failure of a host that could not be connected to, while
	// its packageArtifactUnavailableWait lasts.
	hosts map[string]packageArtifactFailure
	// failures holds the last failure of each reference, until it is read.
	failures map[string]packageArtifactFailure
	// resolved holds, by tag reference, the digest it resolved to last and when, which answers for
	// it for packageArtifactResolvedFor.
	resolved map[string]packageArtifactResolution
	// announced holds the Warnings recorded once: of each problem of each Secret, until it is read.
	announced map[string]bool
}

// newPackageArtifactInspector returns an inspector that reads pullSecrets in namespace, caches what
// it finds in cache, and records its events, through c and recorder.
func newPackageArtifactInspector(c client.Client, recorder events.EventRecorder, namespace string, pullSecrets []string, cache *packageArtifactCache) *packageArtifactInspector {
	return &packageArtifactInspector{
		client:      c,
		recorder:    recorder,
		namespace:   namespace,
		pullSecrets: pullSecrets,
		cache:       cache,
		// A nil transport is http.DefaultTransport, so HTTP(S)_PROXY is honoured. It never retries:
		// a reference that fails is read again after packageArtifactUnavailableWait, and retrying an
		// error status would multiply what a registry failing every repository costs a pass.
		httpClient: &http.Client{Timeout: packageArtifactRequestTimeout},
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[packageArtifactRequest](),
			workqueue.TypedRateLimitingQueueConfig[packageArtifactRequest]{Name: "package-artifact-inspector"},
		),
		hosts:     map[string]packageArtifactFailure{},
		failures:  map[string]packageArtifactFailure{},
		resolved:  map[string]packageArtifactResolution{},
		announced: map[string]bool{},
	}
}

// Start runs the workers until ctx is done.
func (i *packageArtifactInspector) Start(ctx context.Context) error {
	var workers sync.WaitGroup
	for range packageArtifactInspectorWorkers {
		workers.Go(func() {
			for i.processNext(ctx) {
			}
		})
	}
	<-ctx.Done()
	i.queue.ShutDown()
	workers.Wait()
	return nil
}

// NeedLeaderElection keeps the inspector to the leader, whose passes read what it caches.
func (i *packageArtifactInspector) NeedLeaderElection() bool {
	return true
}

// want queues request unless an equal one is queued already.
func (i *packageArtifactInspector) want(request packageArtifactRequest) {
	i.queue.Add(request)
}

// drain processes every queued request, one after another, and returns once the queue is empty.
// It is a test seam: the manager runs the inspector through Start.
func (i *packageArtifactInspector) drain(ctx context.Context) {
	for i.queue.Len() > 0 {
		i.processNext(ctx)
	}
}

// processNext inspects the next queued request and reports false once the queue is shut down. A
// result that could not be cached is queued again after the queue's backoff.
func (i *packageArtifactInspector) processNext(ctx context.Context) bool {
	request, shutdown := i.queue.Get()
	if shutdown {
		return false
	}
	defer i.queue.Done(request)
	if err := i.inspect(ctx, request); err != nil {
		log.FromContext(ctx).Error(err, "caching a package artifact result", "nodewright", request.nodewright, "reference", request.pinned)
		i.queue.AddRateLimited(request)
		return true
	}
	i.queue.Forget(request)
	return true
}

func (i *packageArtifactInspector) timeout() time.Duration {
	if i.requestTimeout == 0 {
		return packageArtifactRequestTimeout
	}
	return i.requestTimeout
}

func (i *packageArtifactInspector) resolveDeadline() time.Duration {
	if i.resolveTimeout == 0 {
		return packageArtifactResolveTimeout
	}
	return i.resolveTimeout
}

func (i *packageArtifactInspector) now() time.Time {
	if i.clock == nil {
		return time.Now()
	}
	return i.clock.Now()
}

// resolve answers, in a pass, what the artifact _package names is: what the cache holds for the
// digest its reference names now. It resolves a tag with one manifest request, whose digest
// answers for the tag for packageArtifactResolvedFor; a containerSHA is the digest, and needs none.
// A digest the cache lacks is queued for inspection, and pending, unless it failed before. A
// reference that could not be read is unavailable for a transient reason and unreadable otherwise,
// and is answered so without a request until it is due to be read again.
func (i *packageArtifactInspector) resolve(ctx context.Context, nodewright *v1alpha1.NodeWright, _package *v1alpha1.Package) packageArtifactOutcome {
	ref, err := packageReference(_package)
	if err != nil {
		return packageArtifactOutcome{Kind: packageArtifactUnreadable, ErrorClass: packageArtifactErrorInvalidReference}
	}
	key := ref.String()
	if outcome, failed := i.remembered(key); failed {
		return outcome
	}
	digest := ref.Digest.String()
	// expires is when the digest a tag resolved to stops answering for it, which the pass is asked
	// to run again by, so a push to the tag is picked up then whatever else the pass waits for.
	var expires time.Time
	if digest == "" {
		digest, expires = i.resolvedDigest(key)
	}
	if digest == "" {
		if class, failed := i.hostFailure(ref.APIHost()); failed {
			return i.fail(ctx, nodewright, _package, key, ref.APIHost(), class, false)
		}
		desc, connected, err := i.resolveDigest(ctx, nodewright, ref)
		if ctx.Err() != nil {
			return packageArtifactOutcome{Kind: packageArtifactUnavailable, ErrorClass: packageArtifactErrorCanceled}
		}
		if err != nil {
			outcome, class := classifyPackageArtifact(nil, err)
			if class == "" {
				class = outcome.Reason
			}
			log.FromContext(ctx).Info("package reference could not be resolved", "reference", key, "class", class, "error", err.Error())
			return i.fail(ctx, nodewright, _package, key, ref.APIHost(), class, connected)
		}
		digest = desc.Digest.String()
		expires = i.resolvedTo(key, digest)
	}
	pinned := ref.Name() + "@" + digest
	if outcome, failed := i.remembered(pinned); failed {
		return outcome
	}
	entry, cached, err := i.cache.get(digest)
	if err != nil {
		log.FromContext(ctx).Error(err, "reading the package artifact cache", "reference", pinned)
	}
	if cached {
		return packageArtifactOutcome{Kind: entry.Kind, Pinned: pinned, Uninstall: entry.Uninstall, Reason: entry.Reason, RetryAt: expires}
	}
	i.want(packageArtifactRequest{nodewright: nodewright.Name, name: _package.Name, version: _package.Version, pinned: pinned})
	// A failure of the digest that is due to be read again answers until the inspection just queued
	// records what it finds, so a package blocked by it is never pending, and its nodes never
	// unblocked and blocked again, for a pass at each retry.
	if outcome, failed := i.lastFailure(pinned); failed {
		outcome.RetryAt = i.now().Add(packageArtifactPendingRequeue)
		return outcome
	}
	return packageArtifactOutcome{Kind: packageArtifactPending, Pinned: pinned}
}

// resolveDigest returns the descriptor of the manifest ref names, read with the pull Secrets, and
// whether a request of it got a connection (connectionTraced).
func (i *packageArtifactInspector) resolveDigest(ctx context.Context, nodewright *v1alpha1.NodeWright, ref artifactclient.Reference) (ocispec.Descriptor, bool, error) {
	credentials := i.credentials(ctx, nodewright, ref)
	ctx, cancel := context.WithTimeout(ctx, i.resolveDeadline())
	defer cancel()
	ctx, connected := connectionTraced(ctx)
	target, err := artifactclient.NewRemote(ref, artifactclient.RemoteOptions{Credentials: credentials, HTTPClient: i.httpClient})
	if err != nil {
		return ocispec.Descriptor{}, false, fmt.Errorf("resolving %s: %w", ref, err)
	}
	desc, err := artifactclient.Resolve(ctx, target)
	if err != nil {
		return ocispec.Descriptor{}, connected.Load(), fmt.Errorf("resolving %s: %w", ref, err)
	}
	return desc, true, nil
}

// connectionTraced returns ctx with a trace that sets the flag it returns once a request sent with
// it gets a connection to a host, past any TLS handshake. A request that failed without one never
// reached its host, which is what packageArtifactInspector.fail keeps the whole host waiting for.
func connectionTraced(ctx context.Context) (context.Context, *atomic.Bool) {
	var connected atomic.Bool
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	}), &connected
}

// remembered answers the last failure of the reference key while it is not due to be read again.
func (i *packageArtifactInspector) remembered(key string) (packageArtifactOutcome, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	f, ok := i.failures[key]
	if !ok || !i.now().Before(f.retryAt) {
		return packageArtifactOutcome{}, false
	}
	return failureOutcome(f), true
}

// lastFailure answers the last failure of the reference key, whether it is due to be read again or
// not.
func (i *packageArtifactInspector) lastFailure(key string) (packageArtifactOutcome, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	f, ok := i.failures[key]
	if !ok {
		return packageArtifactOutcome{}, false
	}
	return failureOutcome(f), true
}

func failureOutcome(f packageArtifactFailure) packageArtifactOutcome {
	kind := packageArtifactUnreadable
	if f.transient {
		kind = packageArtifactUnavailable
	}
	return packageArtifactOutcome{Kind: kind, ErrorClass: f.class, RetryAt: f.retryAt}
}

// fail remembers that the reference key of _package, on host, could not be read for class, and
// answers it; connected is whether its request got a connection to the host. A transient failure
// waits packageArtifactUnavailableWait. One without a connection makes its host wait as long, unless
// the host's window is open already: then the reference waits out that window, which it never
// extends, so references asked for while it lasts cannot keep the host from being asked again. One
// with a connection is the reference's own, and leaves its host open to the others, so one
// repository that keeps failing never keeps the healthy ones of its host from being read. Any other
// failure waits packageArtifactRetryAfter the failures of that class in a row. The first failure of
// an outage, or of each class of other failure, is reported with a Warning on nodewright.
func (i *packageArtifactInspector) fail(ctx context.Context, nodewright *v1alpha1.NodeWright, _package *v1alpha1.Package, key, host, class string, connected bool) packageArtifactOutcome {
	transient := packageArtifactTransient(class)
	now := i.now()
	i.mu.Lock()
	previous, had := i.failures[key]
	f := packageArtifactFailure{class: class, transient: transient, attempts: 1}
	if transient {
		window, open := i.hosts[host]
		switch {
		case connected:
			f.retryAt = now.Add(packageArtifactUnavailableWait)
		case open && now.Before(window.retryAt):
			f.retryAt = window.retryAt
		default:
			f.retryAt = now.Add(packageArtifactUnavailableWait)
			i.hosts[host] = packageArtifactFailure{class: class, transient: true, retryAt: f.retryAt}
		}
	} else {
		if had && !previous.transient && previous.class == class {
			f.attempts = previous.attempts + 1
		}
		f.retryAt = now.Add(packageArtifactRetryAfter(f.attempts))
	}
	i.failures[key] = f
	i.mu.Unlock()

	switch {
	case transient && (!had || !previous.transient):
		warnPackageArtifact(ctx, i.recorder, nodewright, _package, EventsReasonPackageArtifactInspectionFailed,
			fmt.Sprintf("package [%s:%s] reference [%s] could not be read (%s), so the package waits for it, and is read again in %s",
				_package.Name, _package.Version, key, class, f.retryAt.Sub(now)))
	case !transient && (!had || previous.transient || previous.class != class):
		warnPackageArtifact(ctx, i.recorder, nodewright, _package, EventsReasonPackageArtifactInspectionFailed,
			fmt.Sprintf("package [%s:%s] reference [%s] could not be read (%s), so the package is blocked, and is read again in %s; "+
				"a package in a private registry needs its pull Secret in imagePullSecrets",
				_package.Name, _package.Version, key, class, f.retryAt.Sub(now)))
	}
	return failureOutcome(f)
}

// succeeded forgets the failure of the reference key, which was just read.
func (i *packageArtifactInspector) succeeded(key string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.failures, key)
}

// resolvedTo forgets the failure of the tag reference key, which was just resolved to digest,
// remembers that digest for packageArtifactResolvedFor, and returns when it stops answering.
func (i *packageArtifactInspector) resolvedTo(key, digest string) time.Time {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.failures, key)
	now := i.now()
	i.resolved[key] = packageArtifactResolution{digest: digest, at: now}
	return now.Add(packageArtifactResolvedFor)
}

// resolvedDigest returns the digest the tag reference key resolved to less than
// packageArtifactResolvedFor ago, and when it stops answering, or "" when there is none.
func (i *packageArtifactInspector) resolvedDigest(key string) (string, time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()
	r, ok := i.resolved[key]
	expires := r.at.Add(packageArtifactResolvedFor)
	if !ok || !i.now().Before(expires) {
		return "", time.Time{}
	}
	return r.digest, expires
}

// asks reports whether resolving the reference _package names would send a request now: whether it
// is a tag that neither a failure nor a digest it resolved to answers for.
func (i *packageArtifactInspector) asks(_package *v1alpha1.Package) bool {
	ref, err := packageReference(_package)
	if err != nil || ref.Digest != "" {
		return false
	}
	if _, failed := i.remembered(ref.String()); failed {
		return false
	}
	digest, _ := i.resolvedDigest(ref.String())
	return digest == ""
}

func (i *packageArtifactInspector) hostFailure(host string) (string, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	f, ok := i.hosts[host]
	if !ok || !i.now().Before(f.retryAt) {
		return "", false
	}
	return f.class, true
}

// announceOnce reports whether key is announced for the first time, and marks it announced.
func (i *packageArtifactInspector) announceOnce(key string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.announced[key] {
		return false
	}
	i.announced[key] = true
	return true
}

// inspect reads what the digest request pins holds, and caches it: the manifest and, for a package,
// its metadata, never a layer. A digest already cached, or whose reference is not due to be read
// again, and a NodeWright that is gone, are left alone; a failure to read it is remembered, as for
// resolve.
func (i *packageArtifactInspector) inspect(ctx context.Context, request packageArtifactRequest) error {
	ref, err := artifactclient.ParsePinned(request.pinned)
	if err != nil {
		return nil
	}
	digest := ref.Digest.String()
	if _, cached, err := i.cache.get(digest); err == nil && cached {
		return nil
	}
	if _, failed := i.remembered(request.pinned); failed {
		return nil
	}
	nodewright := &v1alpha1.NodeWright{}
	if err := i.client.Get(ctx, types.NamespacedName{Name: request.nodewright}, nodewright); err != nil {
		return client.IgnoreNotFound(err)
	}
	_package := request.pkg()
	host := ref.APIHost()
	if class, failed := i.hostFailure(host); failed {
		i.fail(ctx, nodewright, _package, request.pinned, host, class, false)
		return nil
	}
	inspection, connected, err := i.inspectReference(ctx, nodewright, ref)
	if ctx.Err() != nil {
		return nil
	}
	outcome, class := classifyPackageArtifact(inspection, err)
	if class != "" {
		log.FromContext(ctx).Info("package artifact inspection failed", "reference", request.pinned, "class", class, "error", err.Error())
		i.fail(ctx, nodewright, _package, request.pinned, host, class, connected)
		return nil
	}
	entry := packageArtifactCacheEntry{Kind: outcome.Kind, Uninstall: outcome.Uninstall, Reason: outcome.Reason, Repository: ref.Name(), InspectedAt: i.now()}
	if err := i.cache.put(ctx, digest, entry); err != nil {
		return err
	}
	i.succeeded(request.pinned)
	i.announce(ctx, nodewright, _package, request.pinned, outcome)
	return nil
}

// announce records the event of a digest the inspector just cached: what an artifact declares
// about uninstall, and a rejection. A cached digest is never inspected again, so each is recorded
// once while the cache keeps it.
func (i *packageArtifactInspector) announce(ctx context.Context, nodewright *v1alpha1.NodeWright, _package *v1alpha1.Package, pinned string, outcome packageArtifactOutcome) {
	_, digest, _ := strings.Cut(pinned, "@")
	switch outcome.Kind {
	case packageArtifactAccepted:
		// The action is the package name and the digest, so a re-pushed digest gets its own
		// series instead of folding into the first (see packageEventAction). The webhook bounds
		// a name to 43 characters, so no sha256 digest is cut.
		action := trunstr(_package.Name+"@"+digest, eventActionLengthLimit)
		note := fmt.Sprintf("package [%s:%s] artifact [%s] declares uninstall %s", _package.Name, _package.Version, digest, outcome.Uninstall)
		if spec, ok := nodewright.Spec.Packages[_package.Name]; ok && spec.Version == _package.Version &&
			outcome.Uninstall == artifact.UninstallSupported && !spec.UninstallEnabled() {
			note += "; setting uninstall.enabled to true opts the package in to uninstall"
		}
		log.FromContext(ctx).Info("package artifact uninstall capability", "nodewright", nodewright.Name, "reason", EventsReasonUninstallCapability, "action", action, "note", note)
		i.recorder.Eventf(nodewright, nil, EventTypeNormal, EventsReasonUninstallCapability, action, "%s", note)
	case packageArtifactRejected:
		warnPackageArtifact(ctx, i.recorder, nodewright, _package, EventsReasonPackageArtifactRejected,
			fmt.Sprintf("package [%s:%s] artifact [%s] is rejected: %s, so the package is blocked", _package.Name, _package.Version, pinned, outcome.Reason))
	}
}

// inspectReference reads the manifest of ref and, for a package, its metadata; it never fetches a
// layer. It also returns whether a request of it got a connection (connectionTraced).
func (i *packageArtifactInspector) inspectReference(ctx context.Context, nodewright *v1alpha1.NodeWright, ref artifactclient.Reference) (*artifactclient.Inspection, bool, error) {
	credentials := i.credentials(ctx, nodewright, ref)
	ctx, cancel := context.WithTimeout(ctx, i.timeout())
	defer cancel()
	ctx, connected := connectionTraced(ctx)
	target, err := artifactclient.NewRemote(ref, artifactclient.RemoteOptions{Credentials: credentials, HTTPClient: i.httpClient})
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", ref, err)
	}
	inspection, err := artifactclient.Inspect(ctx, target)
	if err != nil {
		return nil, connected.Load(), fmt.Errorf("inspecting %s: %w", ref, err)
	}
	return inspection, true, nil
}

// credentials returns the credentials for ref that the pull Secrets hold, in the order the registry
// is to be tried with them: the Secrets in order, as the kubelet takes a pod's pull Secrets. They
// are read at every request, so a rotated or added Secret applies to the next one. A Secret that
// cannot be used is passed over, and reported with a Warning on nodewright once until it can be.
func (i *packageArtifactInspector) credentials(ctx context.Context, nodewright *v1alpha1.NodeWright, ref artifactclient.Reference) []artifactclient.Credential {
	sources := make([]artifactclient.CredentialSource, 0, len(i.pullSecrets))
	for _, name := range i.pullSecrets {
		qualified := i.namespace + "/" + name
		secret := &corev1.Secret{}
		if err := i.client.Get(ctx, types.NamespacedName{Namespace: i.namespace, Name: name}, secret); err != nil {
			i.warnPullSecret(ctx, nodewright, packageArtifactActionSecretUnreadable, name,
				"image pull secret %s cannot be read, so package references are read without it: %v", qualified, err)
			continue
		}
		if secret.Type != corev1.SecretTypeDockerConfigJson {
			i.warnPullSecret(ctx, nodewright, packageArtifactActionSecretWrongType, name,
				"image pull secret %s has type %q, not %q, so package references are read without it", qualified, secret.Type, corev1.SecretTypeDockerConfigJson)
			continue
		}
		content, ok := secret.Data[corev1.DockerConfigJsonKey]
		if !ok {
			i.warnPullSecret(ctx, nodewright, packageArtifactActionSecretMissingKey, name,
				"image pull secret %s has no %s key, so package references are read without it", qualified, corev1.DockerConfigJsonKey)
			continue
		}
		i.pullSecretUsable(name)
		sources = append(sources, artifactclient.DockerConfigJSON(qualified, content))
	}
	credentials, skipped := artifactclient.LookupCredential(ref, sources)
	for _, s := range skipped {
		log.FromContext(ctx).V(1).Info("image pull secret holds no credential for a package reference", "secret", s.Source, "reference", ref.String(), "reason", s.Reason)
	}
	return credentials
}

func pullSecretAction(problem, secret string) string {
	return trunstr(problem+":"+secret, eventActionLengthLimit)
}

// pullSecretUsable forgets the Warnings of the pull Secret secret, which was read, so a later
// problem with it is reported again.
func (i *packageArtifactInspector) pullSecretUsable(secret string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, problem := range []string{packageArtifactActionSecretUnreadable, packageArtifactActionSecretWrongType, packageArtifactActionSecretMissingKey} {
		delete(i.announced, "secret|"+pullSecretAction(problem, secret))
	}
}

// warnPullSecret records a Warning about the pull Secret secret on nodewright once until the Secret
// is read, and logs it with the same fields, as warnPackageArtifact does.
func (i *packageArtifactInspector) warnPullSecret(ctx context.Context, nodewright *v1alpha1.NodeWright, problem, secret, note string, args ...any) {
	action := pullSecretAction(problem, secret)
	if !i.announceOnce("secret|" + action) {
		return
	}
	note = fmt.Sprintf(note, args...)
	log.FromContext(ctx).Info("package artifact warning", "nodewright", nodewright.Name, "reason", EventsReasonPackageArtifactPullSecret, "action", action, "note", note)
	i.recorder.Eventf(nodewright, nil, corev1.EventTypeWarning, EventsReasonPackageArtifactPullSecret, action, "%s", note)
}

// classifyPackageArtifact maps what Inspect or Resolve returned to an outcome, and to the class of
// a failure, which is empty when the reference was read. The first matching rule wins.
func classifyPackageArtifact(inspection *artifactclient.Inspection, err error) (packageArtifactOutcome, string) {
	if err == nil {
		switch {
		case inspection.Kind == artifact.KindLegacy:
			return packageArtifactOutcome{Kind: packageArtifactImage, Pinned: inspection.Pinned}, ""
		case len(inspection.Package.HostFiles) > 0:
			return rejectedPackageArtifact(packageArtifactReasonHostFiles), ""
		case inspection.Package.Interrupt != nil:
			return rejectedPackageArtifact(packageArtifactReasonInterrupt), ""
		}
		return packageArtifactOutcome{Kind: packageArtifactAccepted, Pinned: inspection.Pinned, Uninstall: inspection.Package.Uninstall}, ""
	}

	var netErr net.Error
	var errResp *errcode.ErrorResponse
	var invalid *artifact.InvalidArtifactError
	var dnsErr *net.DNSError
	class := packageArtifactErrorOther
	switch {
	case errors.Is(err, context.Canceled):
		class = packageArtifactErrorCanceled
	// An http.Client Timeout surfaces as ErrTransport wrapping a *url.Error whose Timeout() is true.
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		class = packageArtifactErrorTimeout
	// Before the artifact rules: integrity errors take precedence over every reason
	// (docs/user-guide/package-artifact-spec.md), and a metadata blob that does not match its
	// descriptor matches both ErrDigestMismatch and an InvalidArtifactError.
	case errors.Is(err, artifactclient.ErrDigestMismatch):
		class = packageArtifactErrorDigestMismatch
	case errors.Is(err, artifactclient.ErrUnauthorized):
		class = packageArtifactErrorUnauthorized
	case errors.Is(err, artifactclient.ErrNotFound):
		class = packageArtifactErrorNotFound
	// Before the transport rule, which every one of these also matches: each fails the same way at
	// every attempt, so it blocks the package rather than having it wait.
	case untrustedCertificate(err):
		class = packageArtifactErrorTLS
	case errors.Is(err, http.ErrSchemeMismatch):
		class = packageArtifactErrorPlainHTTP
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		class = packageArtifactErrorNoSuchHost
	case errors.Is(err, artifactclient.ErrTransport):
		class = packageArtifactErrorTransport
	// The statuses of a registry, or of the repository behind it, that is busy or down for now.
	case errors.As(err, &errResp) && (errResp.StatusCode == http.StatusRequestTimeout ||
		errResp.StatusCode == http.StatusTooManyRequests || errResp.StatusCode >= http.StatusInternalServerError):
		class = packageArtifactErrorUnavailable
	// Any other client error is about the request, which the next attempt sends again unchanged.
	// The artifact client has made 401, 403 and 404 its sentinels already.
	case errors.As(err, &errResp) && errResp.StatusCode >= http.StatusBadRequest && errResp.StatusCode < http.StatusInternalServerError:
		class = packageArtifactErrorRefused
	case errors.Is(err, artifact.ErrMalformedManifest):
		return rejectedPackageArtifact(packageArtifactReasonMalformedManifest), ""
	case errors.Is(err, artifact.ErrUnsupportedForm):
		return rejectedPackageArtifact(packageArtifactReasonUnsupportedForm), ""
	case errors.Is(err, artifact.ErrUnsupportedVersion):
		return rejectedPackageArtifact(packageArtifactReasonUnsupportedVersion), ""
	case errors.As(err, &invalid):
		return rejectedPackageArtifact(packageArtifactReasonInvalidArtifact + string(invalid.Reason)), ""
	}
	return packageArtifactOutcome{}, class
}

// untrustedCertificate reports whether err is a TLS certificate the operator does not trust, for its
// authority, its host name or itself.
func untrustedCertificate(err error) bool {
	var verification *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verification) || errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

// prune removes from the cache every digest no package of nodewrights names, and forgets the
// references none names. A package names the digest of its containerSHA, or the one its tag
// resolved to last, whatever the age of that resolution. A tag the inspector has not resolved
// since it started, as after a container restart, which keeps the cache and not the memory, cannot
// say which digest it names, so every entry of its repository is kept until it is resolved. It
// sends no request.
func (i *packageArtifactInspector) prune(ctx context.Context, nodewrights []v1alpha1.NodeWright) {
	digests := map[string]bool{}
	tags := map[string]string{}
	repositories := map[string]bool{}
	for _, nodewright := range nodewrights {
		for name := range nodewright.Spec.Packages {
			_package := nodewright.Spec.Packages[name]
			ref, err := packageReference(&_package)
			if err != nil {
				continue
			}
			repositories[ref.Name()] = true
			if ref.Digest != "" {
				digests[ref.Digest.String()] = true
			} else {
				tags[ref.String()] = ref.Name()
			}
		}
	}

	unresolved := map[string]bool{}
	i.mu.Lock()
	for tag, repository := range tags {
		if resolution, ok := i.resolved[tag]; ok {
			digests[resolution.digest] = true
		} else {
			unresolved[repository] = true
		}
	}
	for tag := range i.resolved {
		if _, named := tags[tag]; !named {
			delete(i.resolved, tag)
		}
	}
	for key := range i.failures {
		if !repositories[referenceRepository(key)] {
			delete(i.failures, key)
		}
	}
	i.mu.Unlock()

	removed, err := i.cache.prune(digests, unresolved)
	if err != nil {
		log.FromContext(ctx).Error(err, "pruning the package artifact cache")
	}
	if removed > 0 {
		log.FromContext(ctx).Info("pruned the package artifact cache of digests no NodeWright names", "removed", removed)
	}
}

// referenceRepository returns the registry/repository of the reference key, a tag reference or a
// digest reference as Reference.String writes them.
func referenceRepository(key string) string {
	if name, _, pinned := strings.Cut(key, "@"); pinned {
		return name
	}
	// The tag follows the last colon after the last slash; a colon before it ends a registry port.
	if colon := strings.LastIndex(key, ":"); colon > strings.LastIndex(key, "/") {
		return key[:colon]
	}
	return key
}
