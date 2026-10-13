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
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/NVIDIA/nodewright/artifact"
	artifactclient "github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/operator/api/nodewright/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/clock"
	"oras.land/oras-go/v2/registry/remote/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// packageArtifactRequestTimeout bounds one inspection of one reference, retries included.
	packageArtifactRequestTimeout = 10 * time.Second
	// packageArtifactTagTTL is how long the resolution of a tag is trusted before the registry
	// is asked again.
	packageArtifactTagTTL = 5 * time.Minute
	// packageArtifactFailureBackoff is how long a failed inspection keeps the registry from
	// being asked again. The reconcile pass runs in one worker (MaxConcurrentReconciles 1 in
	// SetupWithManager) and inspects synchronously, and with the per-host failure memo of
	// packageArtifactCache a pass waits at most packageArtifactRequestTimeout per unreachable
	// registry host. So a 5 minute window outlasts any pass in which fewer than 30 hosts time
	// out, and each such host costs one timed-out inspection per window, never one per reference.
	packageArtifactFailureBackoff = 5 * time.Minute
)

// packageArtifactKind is what the operator knows of the artifact a package names.
type packageArtifactKind string

const (
	packageArtifactDisabled packageArtifactKind = "disabled"
	packageArtifactLegacy   packageArtifactKind = "legacy"
	packageArtifactAccepted packageArtifactKind = "artifact"
	packageArtifactRejected packageArtifactKind = "rejected"
	packageArtifactUnknown  packageArtifactKind = "unknown"
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

// Error classes of an unknown outcome.
const (
	packageArtifactErrorInvalidReference = "invalid-reference"
	packageArtifactErrorCanceled         = "canceled"
	packageArtifactErrorTimeout          = "timeout"
	packageArtifactErrorDigestMismatch   = "digest-mismatch"
	packageArtifactErrorUnauthorized     = "unauthorized"
	packageArtifactErrorNotFound         = "not-found"
	packageArtifactErrorTransport        = "transport"
	packageArtifactErrorOther            = "error"
)

// Actions of the EventsReasonPackageArtifactPullSecret Warning. The problem is the action, not
// only the note, because the events.k8s.io broadcaster folds Events with the same type, reason,
// action, reporting controller and instance, regarding and related into one series and never
// sends a later note (getKey and recordToSink in client-go tools/events/event_broadcaster.go).
const (
	packageArtifactActionSecretUnreadable = "SecretUnreadable"
	packageArtifactActionSecretWrongType  = "SecretWrongType"
	packageArtifactActionSecretMissingKey = "SecretMissingKey"
)

// packageArtifactOutcome is what the operator knows of the artifact a package names.
type packageArtifactOutcome struct {
	Kind packageArtifactKind
	// Pinned is the Inspection.Pinned (registry/repository@sha256:hex) of a legacy or accepted
	// outcome. A rejection never carries one: one from a classification error has no Inspection,
	// so neither does one for host files or an interrupt, to keep one rule.
	Pinned string
	// Package is set only when accepted.
	Package *artifact.Package
	// Reason is set only when rejected.
	Reason string
	// ErrorClass is set only when unknown.
	ErrorClass string
}

func unknownPackageArtifact(class string) packageArtifactOutcome {
	return packageArtifactOutcome{Kind: packageArtifactUnknown, ErrorClass: class}
}

func rejectedPackageArtifact(reason string) packageArtifactOutcome {
	return packageArtifactOutcome{Kind: packageArtifactRejected, Reason: reason}
}

// packageArtifactCache keeps what registries answered about the references of packages, so
// that a reconcile pass asks a registry only for an answer it lacks or that has gone stale.
type packageArtifactCache struct {
	client     client.Reader
	recorder   events.EventRecorder
	namespace  string
	pullSecret string

	// Test seams. httpClient sends every registry request; a nil clock is the real clock; a
	// zero requestTimeout is packageArtifactRequestTimeout.
	httpClient     *http.Client
	clock          clock.PassiveClock
	requestTimeout time.Duration

	// mu guards the three stores. Like evictionRefusalsMu it is not load-bearing while
	// MaxConcurrentReconciles is 1, and keeps them safe should that limit be raised.
	mu sync.Mutex
	// digests holds the outcome at each registry/repository@digest for the process lifetime,
	// since the content at a digest cannot change.
	digests map[string]packageArtifactOutcome
	// tags holds, per registry/repository:tag, the outcome of the last refresh that completed.
	tags map[string]packageArtifactResolution
	// failures holds failed refreshes under the reference and, for timeout and transport,
	// under its registry API host as well. A reference always holds a slash and a host never
	// does, so the two kinds of key cannot collide.
	failures map[string]packageArtifactFailure
}

type packageArtifactResolution struct {
	outcome packageArtifactOutcome
	at      time.Time
}

type packageArtifactFailure struct {
	class   string
	expires time.Time
}

// newPackageArtifactCache returns a cache that reads the image pull secret pullSecret in
// namespace through c and records its Warnings with recorder.
func newPackageArtifactCache(c client.Reader, recorder events.EventRecorder, namespace, pullSecret string) *packageArtifactCache {
	return &packageArtifactCache{
		client:     c,
		recorder:   recorder,
		namespace:  namespace,
		pullSecret: pullSecret,
		// A nil base is http.DefaultTransport, so HTTP(S)_PROXY is honoured. The retries on
		// dial timeouts, 408, 429 and 5xx stop once the request context is done, so one
		// inspection stays inside packageArtifactRequestTimeout.
		httpClient: &http.Client{Timeout: packageArtifactRequestTimeout, Transport: retry.NewTransport(nil)},
		digests:    map[string]packageArtifactOutcome{},
		tags:       map[string]packageArtifactResolution{},
		failures:   map[string]packageArtifactFailure{},
	}
}

// packageArtifactOutcome reports what the artifact _package names is. It answers disabled,
// with no request, unless PACKAGE_ARTIFACTS is on and a cache exists. It never returns an
// error: a failure is an unknown outcome. nodewright is only the regarding object of the
// Warning events about the image pull secret.
func (r *SkyhookReconciler) packageArtifactOutcome(ctx context.Context, nodewright *v1alpha1.NodeWright, _package *v1alpha1.Package) packageArtifactOutcome {
	if !r.opts.PackageArtifacts || r.opts.artifactCache == nil {
		return packageArtifactOutcome{Kind: packageArtifactDisabled}
	}
	return r.opts.artifactCache.lookup(ctx, nodewright, _package)
}

// cached answers from the stores alone and never makes a request: the outcome at the digest of
// a containerSHA, or the last completed resolution of a tag, even past packageArtifactTagTTL.
// It reports false on a miss, on an invalid reference, and when only a failure is stored.
func (c *packageArtifactCache) cached(_package *v1alpha1.Package) (packageArtifactOutcome, bool) {
	ref, err := artifactclient.NewReference(_package.Image, _package.Version, _package.ContainerSHA)
	if err != nil {
		return packageArtifactOutcome{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ref.Digest != "" {
		outcome, ok := c.digests[ref.String()]
		return outcome, ok
	}
	resolution, ok := c.tags[ref.String()]
	return resolution.outcome, ok
}

// lookup answers from the stores when they can and inspects the registry otherwise.
func (c *packageArtifactCache) lookup(ctx context.Context, nodewright *v1alpha1.NodeWright, _package *v1alpha1.Package) packageArtifactOutcome {
	// The reference is built as the operator builds the image it runs. One that does not parse
	// is never asked for, and nothing is stored for it.
	ref, err := artifactclient.NewReference(_package.Image, _package.Version, _package.ContainerSHA)
	if err != nil {
		return unknownPackageArtifact(packageArtifactErrorInvalidReference)
	}
	if outcome, ok := c.stored(ref); ok {
		return outcome
	}
	return c.refresh(ctx, nodewright, ref)
}

// stored returns the answer the stores give ref without a request, if they give one.
func (c *packageArtifactCache) stored(ref artifactclient.Reference) (packageArtifactOutcome, bool) {
	now := c.now()
	key := ref.String()
	c.mu.Lock()
	defer c.mu.Unlock()

	// A containerSHA answers from its digest result; a tag from a resolution younger than the TTL.
	var resolution packageArtifactResolution
	resolved := false
	if ref.Digest != "" {
		if outcome, ok := c.digests[key]; ok {
			return outcome, true
		}
	} else if resolution, resolved = c.tags[key]; resolved && now.Sub(resolution.at) < packageArtifactTagTTL {
		return resolution.outcome, true
	}

	// Inside the window of a failure of the reference or of its host, nothing is asked: a tag
	// answers its stale resolution when it has one, anything else the class of the failure.
	class, failed := c.failure(key, now)
	if !failed {
		class, failed = c.failure(ref.APIHost(), now)
	}
	switch {
	case !failed:
		return packageArtifactOutcome{}, false
	case resolved:
		return resolution.outcome, true
	}
	return unknownPackageArtifact(class), true
}

// failure returns the class of the failure stored under key while its window lasts. c.mu must
// be held.
func (c *packageArtifactCache) failure(key string, now time.Time) (string, bool) {
	f, ok := c.failures[key]
	if !ok || !now.Before(f.expires) {
		return "", false
	}
	return f.class, true
}

// refresh inspects ref and stores the result.
//
// A refresh completes when Inspect succeeds or fails with a classification error, which is a
// rejection: a result, never a failure. It deletes the stored failure of ref. For a tag it
// replaces the resolution, so a rejection replaces any earlier legacy or accepted answer and is
// checked again once the TTL lapses, which is how a corrected push to the same tag is picked up.
// For a containerSHA the outcome is kept for the process lifetime. A successful Inspect of a tag
// also stores the outcome under the digest it resolved to.
//
// Any other error fails the refresh. The failure is stored under ref and, for timeout and
// transport, under its host too, so one black-holed or unreachable registry costs one
// timed-out inspection per window whatever number of references it serves. A tag then answers
// its stored resolution, which it keeps.
func (c *packageArtifactCache) refresh(ctx context.Context, nodewright *v1alpha1.NodeWright, ref artifactclient.Reference) packageArtifactOutcome {
	inspection, err := c.inspect(ctx, nodewright, ref)
	// A pass that was cancelled learnt nothing about the registry, so nothing is stored.
	if ctx.Err() != nil {
		return unknownPackageArtifact(packageArtifactErrorCanceled)
	}
	outcome, failureClass := classifyPackageArtifact(inspection, err)
	if failureClass != "" {
		log.FromContext(ctx).Info("package artifact inspection failed", "reference", ref.String(), "class", failureClass, "error", err.Error())
	}

	now := c.now()
	key := ref.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if failureClass == "" {
		delete(c.failures, key)
		if ref.Digest != "" {
			c.digests[key] = outcome
		} else {
			c.tags[key] = packageArtifactResolution{outcome: outcome, at: now}
		}
		if inspection != nil {
			c.digests[inspection.Pinned] = outcome
		}
		return outcome
	}

	failed := packageArtifactFailure{class: failureClass, expires: now.Add(packageArtifactFailureBackoff)}
	c.failures[key] = failed
	if failureClass == packageArtifactErrorTimeout || failureClass == packageArtifactErrorTransport {
		c.failures[ref.APIHost()] = failed
	}
	if resolution, ok := c.tags[key]; ok {
		return resolution.outcome
	}
	return outcome
}

// inspect reads the manifest of ref and, for a package, its metadata. Both are reread on every
// refresh, because the client has no resolve-only call; it never fetches a layer.
func (c *packageArtifactCache) inspect(ctx context.Context, nodewright *v1alpha1.NodeWright, ref artifactclient.Reference) (*artifactclient.Inspection, error) {
	credentials := c.credentials(ctx, nodewright, ref)
	timeout := c.requestTimeout
	if timeout == 0 {
		timeout = packageArtifactRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target, err := artifactclient.NewRemote(ref, artifactclient.RemoteOptions{Credentials: credentials, HTTPClient: c.httpClient})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ref, err)
	}
	inspection, err := artifactclient.Inspect(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("inspecting %s: %w", ref, err)
	}
	return inspection, nil
}

// credentials returns the credentials the image pull secret holds for ref. The Secret is read
// on every inspection, so a rotated Secret applies to the next one. A Secret that cannot be used
// is reported with a Warning on nodewright and the inspection goes on anonymously.
func (c *packageArtifactCache) credentials(ctx context.Context, nodewright *v1alpha1.NodeWright, ref artifactclient.Reference) []artifactclient.Credential {
	if c.pullSecret == "" {
		return nil
	}
	name := c.namespace + "/" + c.pullSecret
	secret := &corev1.Secret{}
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: c.namespace, Name: c.pullSecret}, secret); err != nil {
		c.warnPullSecret(nodewright, packageArtifactActionSecretUnreadable,
			"image pull secret %s cannot be read, so package artifacts are inspected anonymously: %v", name, err)
		return nil
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		c.warnPullSecret(nodewright, packageArtifactActionSecretWrongType,
			"image pull secret %s has type %q, not %q, so package artifacts are inspected anonymously", name, secret.Type, corev1.SecretTypeDockerConfigJson)
		return nil
	}
	content, ok := secret.Data[corev1.DockerConfigJsonKey]
	if !ok {
		c.warnPullSecret(nodewright, packageArtifactActionSecretMissingKey,
			"image pull secret %s has no %s key, so package artifacts are inspected anonymously", name, corev1.DockerConfigJsonKey)
		return nil
	}
	credentials, skipped := artifactclient.LookupCredential(ref, []artifactclient.CredentialSource{artifactclient.DockerConfigJSON(name, content)})
	for _, s := range skipped {
		log.FromContext(ctx).Info("image pull secret holds no credential for a package artifact", "secret", s.Source, "reference", ref.String(), "reason", s.Reason)
	}
	return credentials
}

func (c *packageArtifactCache) warnPullSecret(nodewright *v1alpha1.NodeWright, action, note string, args ...any) {
	c.recorder.Eventf(nodewright, nil, corev1.EventTypeWarning, EventsReasonPackageArtifactPullSecret, action, note, args...)
}

func (c *packageArtifactCache) now() time.Time {
	if c.clock == nil {
		return time.Now()
	}
	return c.clock.Now()
}

// classifyPackageArtifact maps what Inspect returned to an outcome, and to the class of a failed
// refresh, which is empty when the refresh completed. The first matching rule wins.
func classifyPackageArtifact(inspection *artifactclient.Inspection, err error) (packageArtifactOutcome, string) {
	if err == nil {
		switch {
		case inspection.Kind == artifact.KindLegacy:
			return packageArtifactOutcome{Kind: packageArtifactLegacy, Pinned: inspection.Pinned}, ""
		case len(inspection.Package.HostFiles) > 0:
			return rejectedPackageArtifact(packageArtifactReasonHostFiles), ""
		case inspection.Package.Interrupt != nil:
			return rejectedPackageArtifact(packageArtifactReasonInterrupt), ""
		}
		return packageArtifactOutcome{Kind: packageArtifactAccepted, Pinned: inspection.Pinned, Package: inspection.Package}, ""
	}

	var netErr net.Error
	var invalid *artifact.InvalidArtifactError
	class := packageArtifactErrorOther
	switch {
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
	case errors.Is(err, artifactclient.ErrTransport):
		class = packageArtifactErrorTransport
	case errors.Is(err, artifact.ErrMalformedManifest):
		return rejectedPackageArtifact(packageArtifactReasonMalformedManifest), ""
	case errors.Is(err, artifact.ErrUnsupportedForm):
		return rejectedPackageArtifact(packageArtifactReasonUnsupportedForm), ""
	case errors.Is(err, artifact.ErrUnsupportedVersion):
		return rejectedPackageArtifact(packageArtifactReasonUnsupportedVersion), ""
	case errors.As(err, &invalid):
		return rejectedPackageArtifact(packageArtifactReasonInvalidArtifact + string(invalid.Reason)), ""
	}
	return unknownPackageArtifact(class), class
}
