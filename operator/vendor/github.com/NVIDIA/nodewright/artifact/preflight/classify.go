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
	"net"
	"net/http"

	"oras.land/oras-go/v2/registry/remote/errcode"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/client"
)

// Reasons of a rejected outcome.
const (
	ReasonMalformedManifest  = "malformed-manifest"
	ReasonUnsupportedForm    = "unsupported-form"
	ReasonUnsupportedVersion = "unsupported-version"
	// ReasonInvalidArtifact is followed by the artifact.Reason of the rule
	// broken.
	ReasonInvalidArtifact = "invalid-artifact: "
	ReasonHostFiles       = "host-files"
	ReasonInterrupt       = "interrupt"
)

// Error classes of an unknown outcome. ErrorNoResult and ErrorInvalidResult
// are the operator's: a preflight that ended without a result it can use.
const (
	ErrorInvalidReference = "invalid-reference"
	ErrorCanceled         = "canceled"
	ErrorTimeout          = "timeout"
	ErrorDigestMismatch   = "digest-mismatch"
	ErrorUnauthorized     = "unauthorized"
	ErrorNotFound         = "not-found"
	ErrorTransport        = "transport"
	ErrorUnavailable      = "unavailable"
	ErrorOther            = "error"
	ErrorNoResult         = "no-result"
	ErrorInvalidResult    = "invalid-result"
)

// NewResult returns the result of inspecting reference: the outcome Classify
// gives, the credential the registry accepted, and the error as Detail.
func NewResult(reference string, inspection *client.Inspection, err error) Result {
	r := Result{Reference: reference, Outcome: Classify(inspection, err)}
	if err != nil {
		r.Detail = err.Error()
	} else {
		r.Credential = inspection.Credential
	}
	return r
}

// Classify returns the outcome of an inspection, or of the error that ended
// it. An artifact that declares host-files layers or an interrupt is
// rejected, since no released agent installs either.
func Classify(inspection *client.Inspection, err error) Outcome {
	if err == nil {
		switch {
		case inspection.Kind == artifact.KindLegacy:
			return Outcome{Kind: KindImage, Pinned: inspection.Pinned}
		case len(inspection.Package.HostFiles) > 0:
			return rejected(ReasonHostFiles)
		case inspection.Package.Interrupt != nil:
			return rejected(ReasonInterrupt)
		}
		return Outcome{Kind: KindArtifact, Pinned: inspection.Pinned, Uninstall: inspection.Package.Uninstall}
	}

	var netErr net.Error
	var errResp *errcode.ErrorResponse
	var invalid *artifact.InvalidArtifactError
	class := ErrorOther
	switch {
	// An http.Client Timeout surfaces as ErrTransport wrapping a *url.Error whose Timeout() is true.
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		class = ErrorTimeout
	case errors.Is(err, context.Canceled):
		class = ErrorCanceled
	case errors.Is(err, client.ErrInvalidReference):
		class = ErrorInvalidReference
	// Before the artifact rules: integrity errors take precedence over every reason
	// (docs/user-guide/package-artifact-spec.md), and a metadata blob that does not match its
	// descriptor matches both ErrDigestMismatch and an InvalidArtifactError.
	case errors.Is(err, client.ErrDigestMismatch):
		class = ErrorDigestMismatch
	case errors.Is(err, client.ErrUnauthorized):
		class = ErrorUnauthorized
	case errors.Is(err, client.ErrNotFound):
		class = ErrorNotFound
	case errors.Is(err, client.ErrTransport):
		class = ErrorTransport
	// The statuses retry.DefaultPredicate retries, so the registry kept answering one through
	// every retry of the transport.
	case errors.As(err, &errResp) && (errResp.StatusCode == http.StatusRequestTimeout ||
		errResp.StatusCode == http.StatusTooManyRequests || errResp.StatusCode >= http.StatusInternalServerError):
		class = ErrorUnavailable
	case errors.Is(err, artifact.ErrMalformedManifest):
		return rejected(ReasonMalformedManifest)
	case errors.Is(err, artifact.ErrUnsupportedForm):
		return rejected(ReasonUnsupportedForm)
	case errors.Is(err, artifact.ErrUnsupportedVersion):
		return rejected(ReasonUnsupportedVersion)
	case errors.As(err, &invalid):
		return rejected(ReasonInvalidArtifact + string(invalid.Reason))
	}
	return Outcome{Kind: KindUnknown, ErrorClass: class}
}

func rejected(reason string) Outcome {
	return Outcome{Kind: KindRejected, Reason: reason}
}
