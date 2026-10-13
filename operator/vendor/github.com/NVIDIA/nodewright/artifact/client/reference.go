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
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/opencontainers/go-digest"
)

const (
	dockerHub        = "docker.io"
	dockerHubLegacy  = "index.docker.io"
	dockerHubAPIHost = "registry-1.docker.io"
)

// ErrInvalidReference matches every reference NewReference, ParsePinned,
// NewRemote or NewLayout rejects.
var ErrInvalidReference = errors.New("invalid image reference")

var (
	// registryRegexp is the host and optional port of
	// github.com/distribution/reference v0.6.0, which the kubelet parses
	// images with.
	registryRegexp = regexp.MustCompile(`^(?:(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])(?:\.(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9]))*|\[[a-fA-F0-9:]+\])(?::[0-9]+)?$`)
	// repositoryRegexp and tagRegexp are the OCI distribution spec grammars.
	repositoryRegexp = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRegexp        = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)
	// digestRegexp is the pattern of the package containerSHA field.
	digestRegexp = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// imageIDRegexp is a name distribution/reference reserves for image IDs.
	imageIDRegexp = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Reference names one manifest: by Digest when it is set, else by Tag.
// Registry and Repository are normalised as the kubelet normalises an image,
// so alpine is docker.io/library/alpine.
type Reference struct {
	Registry   string
	Repository string
	Tag        string
	Digest     digest.Digest
}

// NewReference returns the Reference of a package from its image, version and
// containerSHA, as the operator builds the image it runs: a set containerSHA
// is the digest, and the version is then neither used nor validated;
// otherwise the version is the tag. The image must carry no tag or digest of
// its own, as the webhook requires.
func NewReference(image, version, containerSHA string) (Reference, error) {
	registry, repository, err := parseName(image)
	if err != nil {
		return Reference{}, err
	}
	ref := Reference{Registry: registry, Repository: repository}
	if containerSHA != "" {
		ref.Digest = digest.Digest(containerSHA)
	} else {
		ref.Tag = version
	}
	if err := ref.validate(); err != nil {
		return Reference{}, err
	}
	return ref, nil
}

// ParsePinned parses registry/repository@sha256:<hex>, normalised as
// NewReference normalises an image. It rejects any tag, even beside a digest.
func ParsePinned(s string) (Reference, error) {
	at := strings.LastIndex(s, "@")
	if at < 0 || at == len(s)-1 {
		return Reference{}, fmt.Errorf("%w: %q has no digest", ErrInvalidReference, s)
	}
	registry, repository, err := parseName(s[:at])
	if err != nil {
		return Reference{}, err
	}
	ref := Reference{Registry: registry, Repository: repository, Digest: digest.Digest(s[at+1:])}
	if err := ref.validate(); err != nil {
		return Reference{}, err
	}
	return ref, nil
}

// parseName splits an image name into its registry and repository the way
// ParseNormalizedNamed and splitDockerDomain of
// github.com/distribution/reference v0.6.0 do. The tag rule is the webhook's:
// a colon after the last slash.
func parseName(name string) (registry, repository string, err error) {
	switch {
	case name == "":
		return "", "", fmt.Errorf("%w: image is empty", ErrInvalidReference)
	case strings.ContainsFunc(name, unicode.IsSpace):
		return "", "", fmt.Errorf("%w: image %q contains whitespace", ErrInvalidReference, name)
	case strings.Contains(name, "@"):
		return "", "", fmt.Errorf("%w: image %q contains a digest", ErrInvalidReference, name)
	case strings.LastIndex(name, ":") > strings.LastIndex(name, "/"):
		return "", "", fmt.Errorf("%w: image %q contains a tag", ErrInvalidReference, name)
	case imageIDRegexp.MatchString(name):
		return "", "", fmt.Errorf("%w: image %q is 64 hexadecimal characters, a form reserved for image IDs", ErrInvalidReference, name)
	}

	registry, repository = dockerHub, name
	if first, rest, ok := strings.Cut(name, "/"); ok &&
		(first == "localhost" || strings.ContainsAny(first, ".:") || strings.ToLower(first) != first) {
		registry, repository = first, rest
	}
	if registry == dockerHubLegacy {
		registry = dockerHub
	}
	if registry == dockerHub && !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}
	return registry, repository, nil
}

func (r Reference) validate() error {
	switch {
	case !registryRegexp.MatchString(r.Registry):
		return fmt.Errorf("%w: registry %q is not a valid host", ErrInvalidReference, r.Registry)
	case !repositoryRegexp.MatchString(r.Repository):
		return fmt.Errorf("%w: repository %q is outside the OCI distribution name grammar", ErrInvalidReference, r.Repository)
	case r.Digest != "":
		if !digestRegexp.MatchString(string(r.Digest)) {
			return fmt.Errorf("%w: digest %q is not sha256 followed by 64 lowercase hexadecimal characters", ErrInvalidReference, r.Digest)
		}
	case !tagRegexp.MatchString(r.Tag):
		return fmt.Errorf("%w: tag %q is not a valid OCI tag", ErrInvalidReference, r.Tag)
	}
	return nil
}

// Name returns registry/repository.
func (r Reference) Name() string {
	return r.Registry + "/" + r.Repository
}

// String returns Name()@digest, or Name():tag when there is no digest.
func (r Reference) String() string {
	if r.Digest != "" {
		return r.Name() + "@" + r.Digest.String()
	}
	return r.Name() + ":" + r.Tag
}

// APIHost returns the host that serves the registry API. It differs from
// Registry only for Docker Hub, whose name is docker.io but whose API is not.
func (r Reference) APIHost() string {
	if r.Registry == dockerHub {
		return dockerHubAPIHost
	}
	return r.Registry
}
