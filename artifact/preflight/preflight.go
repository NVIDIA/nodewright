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

// Package preflight is the result of a preflight stage: what the agent found
// at the reference of a package, inspected on a node with the node's
// credentials. The agent writes it to its container termination message, and
// the operator reads it from the pod status.
package preflight

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/client"
)

const (
	// APIVersion is the version of the result document.
	APIVersion = "nodewright.nvidia.com/preflight/v1"
	// MaxResultSize is the most of a container termination message the
	// kubelet keeps, per TerminationMessagePath in k8s.io/api core/v1.
	MaxResultSize = 4096
	// maxDetailSize bounds Detail, so that a result with a reference of
	// ordinary length always fits MaxResultSize.
	maxDetailSize = 1024
)

// ErrInvalidResult matches every document Decode refuses.
var ErrInvalidResult = errors.New("invalid preflight result")

// Kind is what a reference was found to be.
type Kind string

const (
	// KindImage is a container image, which runs as a package always has.
	KindImage Kind = "image"
	// KindArtifact is a package artifact the agent can install.
	KindArtifact Kind = "artifact"
	// KindRejected is a package artifact that breaks the specification, or
	// declares what this version does not support.
	KindRejected Kind = "rejected"
	// KindUnknown is a reference that could not be inspected.
	KindUnknown Kind = "unknown"
)

// Outcome is what an inspection found at a reference. Pinned is set for an
// image or an artifact, Uninstall for an artifact only, Reason for a
// rejection only and ErrorClass for an unknown outcome only.
type Outcome struct {
	Kind Kind `json:"kind"`
	// Pinned is registry/repository@sha256:<hex>, the digest the reference
	// resolved to, in the form client.ParsePinned accepts.
	Pinned string `json:"pinned,omitempty"`
	// Uninstall is the uninstall declaration of the artifact.
	Uninstall artifact.Uninstall `json:"uninstall,omitempty"`
	// Reason is one of the Reason constants.
	Reason string `json:"reason,omitempty"`
	// ErrorClass is one of the Error constants.
	ErrorClass string `json:"errorClass,omitempty"`
}

// Result is the document a preflight stage reports.
type Result struct {
	APIVersion string `json:"apiVersion"`
	// Reference is the reference inspected, as the request named it, the
	// String of a client.Reference.
	Reference string `json:"reference"`
	Outcome
	// Credential is the source of the credential the registry accepted, or
	// empty when it was read anonymously. It never holds the credential.
	Credential string `json:"credential,omitempty"`
	// Detail is the error behind an unknown or rejected outcome, cut to
	// maxDetailSize bytes.
	Detail string `json:"detail,omitempty"`
}

// Encode returns r as the document the agent writes, with APIVersion set and
// within MaxResultSize. When Detail does not fit, it is left out.
func Encode(r Result) ([]byte, error) {
	r.APIVersion = APIVersion
	r.Detail = strings.ToValidUTF8(r.Detail[:min(len(r.Detail), maxDetailSize)], "")
	data, err := json.Marshal(r)
	if err == nil && len(data) > MaxResultSize && r.Detail != "" {
		r.Detail = ""
		data, err = json.Marshal(r)
	}
	if err != nil {
		return nil, fmt.Errorf("encoding the preflight result: %w", err)
	}
	if len(data) > MaxResultSize {
		return nil, fmt.Errorf("the preflight result of %d bytes exceeds the %d byte limit", len(data), MaxResultSize)
	}
	return data, nil
}

// Decode parses the document the agent wrote for reference. It refuses, with
// ErrInvalidResult, a document over MaxResultSize, of another apiVersion or
// another reference, of an unknown kind, or whose fields do not fit its kind,
// such as a Pinned of another repository or of another digest than the one
// reference names. Fields it does not know are ignored.
func Decode(data []byte, reference string) (Result, error) {
	if len(data) > MaxResultSize {
		return Result{}, fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrInvalidResult, len(data), MaxResultSize)
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrInvalidResult, err)
	}
	if r.APIVersion != APIVersion {
		return Result{}, fmt.Errorf("%w: apiVersion %q is not %q", ErrInvalidResult, r.APIVersion, APIVersion)
	}
	if r.Reference != reference {
		return Result{}, fmt.Errorf("%w: reference %q is not the requested %q", ErrInvalidResult, r.Reference, reference)
	}
	if err := r.validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrInvalidResult, err)
	}
	return r, nil
}

func (r Result) validate() error {
	o := r.Outcome
	switch o.Kind {
	case KindImage, KindArtifact:
		if o.Reason != "" || o.ErrorClass != "" {
			return fmt.Errorf("kind %s has a reason or an error class", o.Kind)
		}
		if o.Kind == KindImage && o.Uninstall != "" {
			return errors.New("kind image has an uninstall declaration")
		}
		if o.Kind == KindArtifact && o.Uninstall != artifact.UninstallSupported && o.Uninstall != artifact.UninstallUnsupported {
			return fmt.Errorf("uninstall %q is neither %q nor %q", o.Uninstall, artifact.UninstallSupported, artifact.UninstallUnsupported)
		}
		return r.validatePinned()
	case KindRejected:
		if o.Reason == "" || o.Pinned != "" || o.Uninstall != "" || o.ErrorClass != "" {
			return errors.New("kind rejected must have a reason and nothing else")
		}
	case KindUnknown:
		if o.ErrorClass == "" || o.Pinned != "" || o.Uninstall != "" || o.Reason != "" {
			return errors.New("kind unknown must have an error class and nothing else")
		}
	default:
		return fmt.Errorf("kind %q is not one of %s, %s, %s or %s", o.Kind, KindImage, KindArtifact, KindRejected, KindUnknown)
	}
	return nil
}

// validatePinned checks that Pinned names the repository of Reference and,
// when Reference names a digest, that digest.
func (r Result) validatePinned() error {
	requested, err := client.ParseReference(r.Reference)
	if err != nil {
		return fmt.Errorf("reference: %w", err)
	}
	pinned, err := client.ParsePinned(r.Pinned)
	if err != nil {
		return fmt.Errorf("pinned: %w", err)
	}
	if pinned.Name() != requested.Name() {
		return fmt.Errorf("pinned %q is not of repository %q", r.Pinned, requested.Name())
	}
	if requested.Digest != "" && pinned.Digest != requested.Digest {
		return fmt.Errorf("pinned %q is not the requested digest %s", r.Pinned, requested.Digest)
	}
	return nil
}
