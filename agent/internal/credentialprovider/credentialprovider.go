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

// Package credentialprovider runs the kubelet's image credential provider
// plugins for a registry reference, as the kubelet of the node runs them for
// an image pull, and turns each response into a client.CredentialSource.
//
// It finds the kubelet among the host's processes, which the agent sees with
// hostPID, and reads its --image-credential-provider-config and
// --image-credential-provider-bin-dir flags: the KubeletConfiguration file
// cannot set either. It runs each plugin whose matchImages match the
// reference with the configured arguments, the kubelet's environment plus the
// configured env, and a CredentialProviderRequest on stdin, as execPlugin of
// k8s.io/kubernetes pkg/credentialprovider/plugin does, without importing it.
package credentialprovider

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/NVIDIA/nodewright/agent/internal/hostfs"
	"github.com/NVIDIA/nodewright/artifact/client"
)

const (
	// DefaultProcRoot is where the agent sees the host's processes.
	DefaultProcRoot = "/proc"
	// SourceName labels the reason no plugin runs at all.
	SourceName = "kubelet credential provider plugins"

	kubeletBinary = "kubelet"
	configFlag    = "image-credential-provider-config"
	binDirFlag    = "image-credential-provider-bin-dir"

	// execTimeout is the kubelet's catch-all timeout for one plugin run.
	execTimeout = time.Minute

	configGroup  = "kubelet.config.k8s.io"
	configKind   = "CredentialProviderConfig"
	pluginGroup  = "credentialprovider.kubelet.k8s.io"
	requestKind  = "CredentialProviderRequest"
	responseKind = "CredentialProviderResponse"
	sourcePrefix = "credential provider "
)

// versions are the API versions the kubelet reads a CredentialProviderConfig
// and talks to a plugin in.
var versions = []string{"v1", "v1beta1", "v1alpha1"}

// cacheKeyTypes are the cacheKeyType values a response must have.
var cacheKeyTypes = []string{"Image", "Registry", "Global"}

// Host is the node whose kubelet's plugins run.
type Host struct {
	// RootMount is where the host's root filesystem is mounted.
	RootMount string
	// ProcRoot lists the host's processes. Empty means DefaultProcRoot.
	ProcRoot string
	// Runner runs a plugin. Nil means ExecRunner(RootMount).
	Runner Runner
}

// Command is one run of a plugin.
type Command struct {
	// Path is the plugin executable, a host path.
	Path string
	Args []string
	// Env is the whole environment of the plugin.
	Env []string
	// Stdin is the CredentialProviderRequest.
	Stdin []byte
}

// Runner runs a plugin to completion, or until ctx ends, and returns its
// standard output. Its errors never quote that output, which holds
// credentials.
type Runner func(ctx context.Context, cmd Command) ([]byte, error)

type kubelet struct {
	pid     int
	environ []string
	// config and binDir are host paths.
	config string
	binDir string
}

type providerConfig struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Providers  []provider `json:"providers"`
}

type provider struct {
	Name        string   `json:"name"`
	MatchImages []string `json:"matchImages"`
	APIVersion  string   `json:"apiVersion"`
	Args        []string `json:"args"`
	Env         []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
	TokenAttributes *struct{} `json:"tokenAttributes"`
}

type request struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Image      string `json:"image"`
}

type response struct {
	APIVersion    string  `json:"apiVersion"`
	Kind          string  `json:"kind"`
	CacheKeyType  string  `json:"cacheKeyType"`
	CacheDuration *string `json:"cacheDuration"`
	Auth          map[string]struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"auth"`
}

// Sources returns a source holding the response of each plugin of the
// kubelet whose matchImages match ref, in the order of the kubelet's
// configuration, each labelled "credential provider <name>", and every
// plugin it skipped with why. When no plugin can run at all, such as when no
// kubelet runs or it configures no plugins, it returns one SkippedSource
// named SourceName that says why.
//
// A plugin that the kubelet runs with a service account token of the pod it
// pulls for, as tokenAttributes asks, is skipped: the agent has no such pod.
// So is one that fails, outlives ctx or the kubelet's timeout of a minute, or
// answers what the kubelet would refuse. No reason quotes a response.
func Sources(ctx context.Context, ref client.Reference, host Host) ([]client.CredentialSource, []client.SkippedSource) {
	k, providers, err := host.configuration()
	if err != nil {
		return nil, []client.SkippedSource{{Source: SourceName, Reason: err.Error()}}
	}
	runner := host.Runner
	if runner == nil {
		runner = ExecRunner(host.RootMount)
	}
	var sources []client.CredentialSource
	var skipped []client.SkippedSource
	for _, p := range providers {
		content, reason := k.provide(ctx, runner, p, ref)
		if reason != "" {
			skipped = append(skipped, client.SkippedSource{Source: sourcePrefix + p.Name, Reason: reason})
			continue
		}
		sources = append(sources, client.DockerConfigJSON(sourcePrefix+p.Name, content))
	}
	return sources, skipped
}

// configuration returns the kubelet and the providers its configuration
// lists.
func (host Host) configuration() (kubelet, []provider, error) {
	k, err := findKubelet(cmp.Or(host.ProcRoot, DefaultProcRoot))
	if err != nil {
		return kubelet{}, nil, err
	}
	if k.config == "" || k.binDir == "" {
		return kubelet{}, nil, fmt.Errorf("the kubelet, process %d, runs no plugins: it does not set both --%s and --%s", k.pid, configFlag, binDirFlag)
	}
	if !filepath.IsAbs(k.binDir) {
		return kubelet{}, nil, fmt.Errorf("the kubelet's --%s %q is not absolute", binDirFlag, k.binDir)
	}
	config, err := hostfs.HostPathToMounted(host.RootMount, k.config)
	if err != nil {
		return kubelet{}, nil, fmt.Errorf("resolving the kubelet's --%s: %w", configFlag, err)
	}
	providers, err := readConfig(config)
	if err != nil {
		return kubelet{}, nil, err
	}
	return k, providers, nil
}

// findKubelet returns the kubelet process of lowest PID beneath procRoot,
// with the credential provider flags it was started with and its
// environment.
func findKubelet(procRoot string) (kubelet, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return kubelet{}, fmt.Errorf("listing processes: %w", err)
	}
	var k kubelet
	var args []string
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || (k.pid != 0 && pid > k.pid) {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline"))
		if err != nil {
			// The process exited after the listing.
			continue
		}
		argv := splitNUL(cmdline)
		if len(argv) > 0 && filepath.Base(argv[0]) == kubeletBinary {
			k.pid, args = pid, argv
		}
	}
	if k.pid == 0 {
		return kubelet{}, fmt.Errorf("no %s process in %s", kubeletBinary, procRoot)
	}
	k.config = flagValue(args, configFlag)
	k.binDir = flagValue(args, binDirFlag)
	environ, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(k.pid), "environ"))
	if err != nil {
		return kubelet{}, fmt.Errorf("reading the environment of the kubelet, process %d: %w", k.pid, err)
	}
	k.environ = splitNUL(environ)
	return k, nil
}

func splitNUL(data []byte) []string {
	var fields []string
	for field := range strings.SplitSeq(string(data), "\x00") {
		if field != "" {
			fields = append(fields, field)
		}
	}
	return fields
}

// flagValue returns the value args, a kubelet's argv, give the flag name, in
// either form the kubelet's flag parsing takes, --name=value or --name value,
// with _ allowed for - in the name. The last one wins, and a bare -- ends the
// flags.
func flagValue(args []string, name string) string {
	var value string
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		flag, ok := strings.CutPrefix(args[i], "--")
		if !ok {
			continue
		}
		flag, inline, hasInline := strings.Cut(flag, "=")
		if strings.ReplaceAll(flag, "_", "-") != name {
			continue
		}
		switch {
		case hasInline:
			value = inline
		case i+1 < len(args):
			i++
			value = args[i]
		}
	}
	return value
}

// readConfig reads the CredentialProviderConfig at path or, when path is a
// directory, every .json, .yaml and .yml file in it in lexical order, merging
// their providers, as readCredentialProviderConfig of the kubelet does.
func readConfig(path string) ([]provider, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading the credential provider config: %w", err)
	}
	files := []string{path}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("reading the credential provider config directory: %w", err)
		}
		files = nil
		for _, entry := range entries {
			switch filepath.Ext(entry.Name()) {
			case ".json", ".yaml", ".yml":
				if !entry.IsDir() {
					files = append(files, filepath.Join(path, entry.Name()))
				}
			}
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("the credential provider config directory %s holds no .json, .yaml or .yml file", path)
		}
	}
	var providers []provider
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading the credential provider config: %w", err)
		}
		var config providerConfig
		// No decoding error is quoted: one can quote a value, and a provider's
		// env can hold a secret.
		if decode(data, &config) != nil {
			return nil, fmt.Errorf("the credential provider config %s is malformed", file)
		}
		if config.Kind != configKind || !supported(config.APIVersion, configGroup) {
			return nil, fmt.Errorf("the credential provider config %s has apiVersion %q and kind %q, not a %s of %s",
				file, config.APIVersion, config.Kind, configKind, configGroup)
		}
		for _, p := range config.Providers {
			if slices.ContainsFunc(providers, func(seen provider) bool { return seen.Name == p.Name }) {
				return nil, fmt.Errorf("the credential provider configs name provider %q twice", p.Name)
			}
			providers = append(providers, p)
		}
	}
	return providers, nil
}

// decode decodes JSON, or YAML through JSON as sigs.k8s.io/yaml does for the
// kubelet, so that both take the json field names.
func decode(data []byte, v any) error {
	if !json.Valid(data) {
		var document any
		if err := yaml.Unmarshal(data, &document); err != nil {
			return err
		}
		var err error
		if data, err = json.Marshal(document); err != nil {
			return err
		}
	}
	return json.Unmarshal(data, v)
}

func supported(apiVersion, group string) bool {
	g, version, _ := strings.Cut(apiVersion, "/")
	return g == group && slices.Contains(versions, version)
}

// provide runs p for ref and returns its response as a Docker config, or why
// it gives none.
func (k kubelet) provide(ctx context.Context, runner Runner, p provider, ref client.Reference) ([]byte, string) {
	if !slices.ContainsFunc(p.MatchImages, func(pattern string) bool { return client.MatchImage(pattern, ref) }) {
		return nil, fmt.Sprintf("no matchImages entry matches %s", ref.Name())
	}
	if p.TokenAttributes != nil {
		return nil, "tokenAttributes asks for a service account token of the pod an image is pulled for, which the agent has no pod to request"
	}
	if !supported(p.APIVersion, pluginGroup) {
		return nil, fmt.Sprintf("apiVersion %q is not a version of %s", p.APIVersion, pluginGroup)
	}
	if err := hostfs.ValidatePathComponent("provider name", p.Name); err != nil {
		return nil, err.Error()
	}
	stdin, err := json.Marshal(request{APIVersion: p.APIVersion, Kind: requestKind, Image: ref.Name()})
	if err != nil {
		return nil, fmt.Sprintf("encoding the request: %v", err)
	}
	env := slices.Clone(k.environ)
	for _, v := range p.Env {
		env = append(env, v.Name+"="+v.Value)
	}
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	stdout, err := runner(ctx, Command{Path: filepath.Join(k.binDir, p.Name), Args: p.Args, Env: env, Stdin: stdin})
	if err != nil {
		return nil, err.Error()
	}
	return dockerConfig(stdout, p.APIVersion)
}

// dockerConfig returns the auth of a plugin's response as a Docker config,
// or why the kubelet would refuse the response, quoting none of it. The
// kubelet decodes a response strictly, so an unknown field or trailing data
// refuses it too.
func dockerConfig(stdout []byte, apiVersion string) ([]byte, string) {
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	decoder.DisallowUnknownFields()
	var r response
	if decoder.Decode(&r) != nil {
		return nil, "the response is not a CredentialProviderResponse"
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, "the response is followed by more output"
	}
	switch {
	case r.APIVersion != apiVersion:
		return nil, fmt.Sprintf("the response apiVersion is not the configured %s", apiVersion)
	case r.Kind != responseKind:
		return nil, fmt.Sprintf("the response kind is not %s", responseKind)
	case !slices.Contains(cacheKeyTypes, r.CacheKeyType):
		return nil, fmt.Sprintf("the response cacheKeyType is not one of %s", strings.Join(cacheKeyTypes, ", "))
	}
	if r.CacheDuration != nil {
		if _, err := time.ParseDuration(*r.CacheDuration); err != nil {
			return nil, "the response cacheDuration is not a duration"
		}
	}
	content, err := json.Marshal(map[string]any{"auths": r.Auth})
	if err != nil {
		return nil, "encoding the response as a Docker config failed"
	}
	return content, ""
}
