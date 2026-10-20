/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nodewright/agent/internal/interrupts"
	agentversion "github.com/NVIDIA/nodewright/agent/internal/version"
	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/client"
	"github.com/NVIDIA/nodewright/artifact/registrytest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	artifactTestRepository = "packages/demo"
	artifactTestUsername   = "registrytest-user"
	artifactTestPassword   = "registrytest-password"
	artifactMarkerEnv      = "NODEWRIGHT_AGENT_TEST_MARKER_DIR"
)

var _ = Describe("package artifact installation", func() {
	var (
		root      string
		markerDir string
	)

	BeforeEach(func() {
		GinkgoT().Setenv(copyResolverEnv, "false")
		GinkgoT().Setenv(alwaysRunEnv, "false")
		GinkgoT().Setenv(resourceIDEnv, "resource_package_1.0.0")
		GinkgoT().Setenv(stateRootEnv, "/state")
		GinkgoT().Setenv(logRootEnv, "/logs")
		GinkgoT().Setenv(writeLogsEnv, "false")
		unsetEnvironment(packageRefEnv)
		unsetEnvironment(registryAuthFilesEnv)
		root = GinkgoT().TempDir()
		markerDir = GinkgoT().TempDir()
		GinkgoT().Setenv(artifactMarkerEnv, markerDir)
		// The legacy path fails on a missing data directory, so every success
		// below shows that the artifact path ran.
		GinkgoT().Setenv(dataDirEnv, filepath.Join(GinkgoT().TempDir(), "missing"))
	})

	It("reads the package reference and registry auth files into the runtime", func() {
		GinkgoT().Setenv(packageRefEnv, "registry.example.com/package@sha256:"+strings.Repeat("a", 64))
		GinkgoT().Setenv(registryAuthFilesEnv, "/var/run/secrets/registry-0/.dockerconfigjson::/var/run/secrets/registry-1/.dockerconfigjson")

		runtime := runtimeFromEnvironment(io.Discard, io.Discard, nil)

		Expect(runtime.packageRef).To(Equal("registry.example.com/package@sha256:" + strings.Repeat("a", 64)))
		Expect(runtime.registryAuthFiles).To(Equal([]string{
			"/var/run/secrets/registry-0/.dockerconfigjson",
			"/var/run/secrets/registry-1/.dockerconfigjson",
		}))
	})

	It("treats empty artifact variables as unset and prepares legacy package data", func() {
		GinkgoT().Setenv(packageRefEnv, "")
		GinkgoT().Setenv(registryAuthFilesEnv, "")
		dataDir := GinkgoT().TempDir()
		writePackageFixture(dataDir, "[]", true)
		GinkgoT().Setenv(dataDirEnv, dataDir)

		runtime := runtimeFromEnvironment(io.Discard, io.Discard, nil)
		exitCode, stderr := runArtifactAgent(nil, "config", root, "/package")

		Expect(runtime.packageRef).To(BeEmpty())
		Expect(runtime.registryAuthFiles).To(BeEmpty())
		Expect(exitCode).To(Equal(ExitSuccess), stderr)
		Expect(filepath.Join(root, "package", configFileName)).To(BeARegularFile())
	})

	It("orders the registry credential sources as the kubelet does", func() {
		ref, err := client.ParseReference("registry.example.com/org/app:1.0")
		Expect(err).NotTo(HaveOccurred())
		procRoot := writeFakeKubelet(root, fakeProviderConfig("registry.example.com"))
		writeFakePlugin(root, fakeResponse("other-registry.example.com"))
		agent := New(WithProcRoot(procRoot), WithCredentialProviderRunner(fakePluginRunner(root))).(orchestrator)

		sources, skipped, err := agent.credentialSources(context.Background(), ref, root, []string{"/first.json", "/second.json"})
		Expect(err).NotTo(HaveOccurred())
		Expect(skipped).To(BeEmpty())
		_, skipped = client.LookupCredential(ref, sources)

		var order []string
		for _, source := range skipped {
			order = append(order, source.Source)
		}
		Expect(order).To(Equal([]string{
			"/first.json",
			"/second.json",
			filepath.Join(root, "var", "lib", "kubelet", "config.json"),
			filepath.Join(root, "root", ".docker", "config.json"),
			filepath.Join(root, ".docker", "config.json"),
			"credential provider " + fakePluginName,
		}))
	})

	Describe("registry credentials", func() {
		var (
			reg       *registrytest.Registry
			authValue string
		)

		BeforeEach(func() {
			reg = newArtifactRegistry(registrytest.Options{
				Username: artifactTestUsername,
				Password: artifactTestPassword,
			})
			GinkgoT().Setenv(packageRefEnv, pushArtifact(reg, supportedPackage(writeArtifactPayload())))
			authValue = base64.StdEncoding.EncodeToString([]byte(artifactTestUsername + ":" + artifactTestPassword))
		})

		expectNoSecret := func(stderr string) {
			GinkgoHelper()
			Expect(stderr).NotTo(ContainSubstring(artifactTestPassword))
			Expect(stderr).NotTo(ContainSubstring(authValue))
		}

		It("authenticates with the registry auth file alone", func() {
			authFile := filepath.Join(GinkgoT().TempDir(), "auth.json")
			writeDockerConfig(authFile, reg.Host())
			GinkgoT().Setenv(registryAuthFilesEnv, authFile)

			exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(filepath.Join(root, "package", configFileName)).To(BeARegularFile())
			Expect(stderr).To(ContainSubstring("registry credentials to try in order"))
			Expect(stderr).To(ContainSubstring(authFile))
			expectNoSecret(stderr)
		})

		It("authenticates with the node's kubelet config", func() {
			nodeConfig := filepath.Join(root, "var", "lib", "kubelet", "config.json")
			writeDockerConfig(nodeConfig, reg.Host())

			exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(filepath.Join(root, "package", configFileName)).To(BeARegularFile())
			Expect(stderr).To(ContainSubstring(nodeConfig))
			expectNoSecret(stderr)
		})

		It("falls through an auth file without an entry for the registry to the node config", func() {
			authFile := filepath.Join(GinkgoT().TempDir(), "auth.json")
			writeDockerConfig(authFile, "other-registry.example.com")
			GinkgoT().Setenv(registryAuthFilesEnv, filepath.Join(GinkgoT().TempDir(), "missing.json")+":"+authFile)
			writeDockerConfig(filepath.Join(root, "var", "lib", "kubelet", "config.json"), reg.Host())

			exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(filepath.Join(root, "package", configFileName)).To(BeARegularFile())
			Expect(stderr).To(ContainSubstring("registry credential source skipped"))
			Expect(stderr).To(ContainSubstring(authFile))
			Expect(stderr).To(ContainSubstring("no auths entry for"))
			Expect(stderr).To(ContainSubstring("file does not exist"))
			expectNoSecret(stderr)
		})

		It("authenticates with a kubelet credential provider plugin", func() {
			procRoot := writeFakeKubelet(root, fakeProviderConfig(reg.Host()))
			writeFakePlugin(root, fakeResponse(reg.Host()))

			exitCode, stderr := runAgentWith(
				reg,
				[]Option{WithProcRoot(procRoot), WithCredentialProviderRunner(fakePluginRunner(root))},
				"apply", root, "/package",
			)

			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(filepath.Join(root, "package", configFileName)).To(BeARegularFile())
			Expect(stderr).To(ContainSubstring("credential provider " + fakePluginName))
			expectNoSecret(stderr)
		})

		It("fails unauthorized without credentials", func() {
			exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

			Expect(exitCode).To(Equal(ExitFailure), stderr)
			Expect(stderr).To(ContainSubstring("agent execution failed"))
			Expect(stderr).To(ContainSubstring("reading the package artifact anonymously"))
			Expect(stderr).To(ContainSubstring("unauthorized"))
			Expect(filepath.Join(root, "package")).NotTo(BeAnExistingFile())
			expectNoSecret(stderr)
		})
	})

	DescribeTable(
		"refuses an artifact this agent version cannot install",
		func(push func(*registrytest.Registry) string, phrases ...string) {
			reg := newArtifactRegistry(registrytest.Options{})
			GinkgoT().Setenv(packageRefEnv, push(reg))

			exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

			Expect(exitCode).To(Equal(ExitFailure), stderr)
			Expect(stderr).To(ContainSubstring("agent execution failed"))
			for _, phrase := range phrases {
				Expect(stderr).To(ContainSubstring(phrase))
			}
			Expect(filepath.Join(root, "package")).NotTo(BeAnExistingFile())
		},
		Entry("a container image", func(reg *registrytest.Registry) string {
			desc, err := registrytest.PushImage(
				context.Background(),
				reg.Repository(artifactTestRepository),
				"image",
				writeArtifactPayload(),
			)
			Expect(err).NotTo(HaveOccurred())
			return reg.Host() + "/" + artifactTestRepository + "@" + desc.Digest.String()
		}, "is a container image, not a package artifact"),
		Entry("metadata outside the schema", func(reg *registrytest.Registry) string {
			spec := supportedPackage(writeArtifactPayload())
			spec.Uninstall = "optional"
			return pushArtifact(reg, spec)
		}, "metadata-schema"),
		Entry("host-files layers", func(reg *registrytest.Registry) string {
			spec := supportedPackage(writeArtifactPayload())
			spec.HostFiles = hostFilesLayers()
			return pushArtifact(reg, spec)
		}, "declares host-files layers, which this agent version does not support (host-files)"),
		Entry("an interrupt", func(reg *registrytest.Registry) string {
			spec := supportedPackage(writeArtifactPayload())
			spec.Interrupt = interruptDeclaration()
			return pushArtifact(reg, spec)
		}, "declares an interrupt, which this agent version does not support (interrupt)"),
		Entry("host-files layers and an interrupt, by the first reason the operator's preflight gives", func(reg *registrytest.Registry) string {
			spec := supportedPackage(writeArtifactPayload())
			spec.HostFiles = hostFilesLayers()
			spec.Interrupt = interruptDeclaration()
			return pushArtifact(reg, spec)
		}, "declares host-files layers, which this agent version does not support (host-files)"),
	)

	It("refuses only the uninstall stages of a package that declares uninstall unsupported", func() {
		reg := newArtifactRegistry(registrytest.Options{})
		spec := supportedPackage(writeArtifactPayload())
		spec.Uninstall = artifact.UninstallUnsupported
		GinkgoT().Setenv(packageRefEnv, pushArtifact(reg, spec))

		for _, mode := range []string{"uninstall", "uninstall-check"} {
			exitCode, stderr := runArtifactAgent(reg, mode, root, "/package")

			Expect(exitCode).To(Equal(ExitFailure), stderr)
			Expect(stderr).To(ContainSubstring("declares uninstall unsupported"))
			Expect(filepath.Join(root, "package")).NotTo(BeAnExistingFile())
		}

		exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

		Expect(exitCode).To(Equal(ExitSuccess), stderr)
	})

	It("rejects a package reference without a digest before reading the registry", func() {
		reg := newArtifactRegistry(registrytest.Options{})
		pushArtifact(reg, supportedPackage(writeArtifactPayload()))
		reg.ResetRequests()
		GinkgoT().Setenv(packageRefEnv, reg.Host()+"/"+artifactTestRepository+":1.0.0")

		exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

		Expect(exitCode).To(Equal(ExitFailure), stderr)
		Expect(stderr).To(ContainSubstring(packageRefEnv))
		Expect(stderr).To(ContainSubstring("invalid image reference"))
		Expect(reg.Requests()).To(BeEmpty())
		Expect(filepath.Join(root, "package")).NotTo(BeAnExistingFile())
	})

	Describe("package layout", func() {
		var (
			reg      *registrytest.Registry
			copyRoot string
		)

		BeforeEach(func() {
			reg = newArtifactRegistry(registrytest.Options{})
			GinkgoT().Setenv(packageRefEnv, pushArtifact(reg, supportedPackage(writeArtifactPayload())))
			copyRoot = filepath.Join(root, "package")
		})

		checkMarker := func() string {
			GinkgoHelper()
			data, err := os.ReadFile(filepath.Join(markerDir, "check"))
			Expect(err).NotTo(HaveOccurred())
			return strings.TrimSpace(string(data))
		}

		It("reuses an existing package for a check stage and an interrupt without reading the registry", func() {
			writeArtifactPackage(copyRoot, "seeded")
			encoded, err := interrupts.Encode(interrupts.NoOp{})
			Expect(err).NotTo(HaveOccurred())
			reg.ResetRequests()

			exitCode, stderr := runArtifactAgent(reg, "apply-check", root, "/package")
			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(stderr).To(ContainSubstring("reusing the laid-out package"))
			Expect(checkMarker()).To(Equal("seeded"))

			exitCode, stderr = runArtifactAgent(reg, "interrupt", root, "/package", encoded)
			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(stderr).To(ContainSubstring("reusing the laid-out package"))

			Expect(reg.Requests()).To(BeEmpty())
		})

		It("lays out the package for a check stage when none exists", func() {
			exitCode, stderr := runArtifactAgent(reg, "apply-check", root, "/package")

			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(checkMarker()).To(Equal("payload"))
			Expect(filepath.Join(copyRoot, configFileName)).To(BeARegularFile())
			Expect(reg.Requests()).NotTo(BeEmpty())
		})

		It("lays the package out over an existing one for a stage invocation, keeping what the payload lacks", func() {
			writeArtifactPackage(copyRoot, "seeded")
			Expect(os.WriteFile(filepath.Join(copyRoot, "state"), []byte("state"), 0o600)).To(Succeed())

			exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(os.ReadFile(filepath.Join(copyRoot, "state"))).To(Equal([]byte("state")))
			Expect(filepath.Join(copyRoot, configFileName)).To(BeARegularFile())
			Expect(os.ReadFile(filepath.Join(copyRoot, "skyhook_dir", "apply-check"))).To(
				ContainSubstring("echo payload"),
			)
		})

		It("lays out the package when the package data directory does not exist", func() {
			exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

			Expect(exitCode).To(Equal(ExitSuccess), stderr)
			Expect(filepath.Join(copyRoot, configFileName)).To(BeARegularFile())
		})
	})

	It("overlays mounted package data files without copying directory modes", func() {
		payload := writeArtifactPayload()
		payloadConfigMaps := filepath.Join(payload, configMapsDirName)
		Expect(os.Mkdir(payloadConfigMaps, 0o755)).To(Succeed())
		Expect(os.Chmod(payloadConfigMaps, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(payloadConfigMaps, "settings.conf"), []byte("default"), 0o644)).To(Succeed())
		reg := newArtifactRegistry(registrytest.Options{})
		GinkgoT().Setenv(packageRefEnv, pushArtifact(reg, supportedPackage(payload)))

		dataDir := GinkgoT().TempDir()
		Expect(os.Chmod(dataDir, 0o700)).To(Succeed())
		mountedConfigMaps := filepath.Join(dataDir, configMapsDirName)
		Expect(os.Mkdir(mountedConfigMaps, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(mountedConfigMaps, "settings.conf"), []byte("mounted"), 0o644)).To(Succeed())
		Expect(os.Chmod(filepath.Join(mountedConfigMaps, "settings.conf"), 0o644)).To(Succeed())
		// Laid out as the kubelet atomic writer lays out a configMap volume.
		nodeMetadata := filepath.Join(dataDir, "node-metadata")
		timestamped := "..2026_10_12_00_00_00.000000001"
		Expect(os.MkdirAll(filepath.Join(nodeMetadata, timestamped), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(nodeMetadata, timestamped, "labels"), []byte("zone=a"), 0o644)).To(Succeed())
		Expect(os.Symlink(timestamped, filepath.Join(nodeMetadata, "..data"))).To(Succeed())
		Expect(os.Symlink(filepath.Join("..data", "labels"), filepath.Join(nodeMetadata, "labels"))).To(Succeed())
		Expect(os.Chmod(mountedConfigMaps, 0o777)).To(Succeed())
		Expect(os.Chmod(nodeMetadata, 0o777)).To(Succeed())
		GinkgoT().Setenv(dataDirEnv, dataDir)
		fresh := filepath.Join(GinkgoT().TempDir(), "fresh")
		Expect(os.Mkdir(fresh, 0o755)).To(Succeed())
		freshMode := permissions(fresh)

		exitCode, stderr := runArtifactAgent(reg, "apply", root, "/package")

		Expect(exitCode).To(Equal(ExitSuccess), stderr)
		copyRoot := filepath.Join(root, "package")
		Expect(permissions(copyRoot)).To(Equal(fs.FileMode(0o755)))
		Expect(permissions(filepath.Join(copyRoot, configMapsDirName))).To(Equal(fs.FileMode(0o755)))
		Expect(permissions(filepath.Join(copyRoot, "node-metadata"))).To(Equal(freshMode))
		Expect(permissions(filepath.Join(copyRoot, "node-metadata"))).NotTo(Equal(fs.FileMode(0o777)))
		Expect(os.ReadFile(filepath.Join(copyRoot, configMapsDirName, "settings.conf"))).To(Equal([]byte("mounted")))
		labels := filepath.Join(copyRoot, "node-metadata", "labels")
		info, err := os.Lstat(labels)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().IsRegular()).To(BeTrue())
		Expect(os.ReadFile(labels)).To(Equal([]byte("zone=a")))
	})

	It("prints the version without reading the package reference", func() {
		reg := newArtifactRegistry(registrytest.Options{})
		GinkgoT().Setenv(packageRefEnv, pushArtifact(reg, supportedPackage(writeArtifactPayload())))
		reg.ResetRequests()
		stdout := &bytes.Buffer{}

		exitCode := New(WithRegistryHTTPClient(reg.Client())).Run(
			context.Background(),
			[]string{versionArgument},
			stdout,
			io.Discard,
		)

		Expect(exitCode).To(Equal(ExitSuccess))
		Expect(stdout.String()).To(Equal(agentversion.GetVersion() + "\n"))
		Expect(reg.Requests()).To(BeEmpty())
		Expect(os.ReadDir(root)).To(BeEmpty())
	})
})

// runArtifactAgent runs the agent against reg, or with the default registry
// client when reg is nil, and returns its exit code and stderr.
func runArtifactAgent(reg *registrytest.Registry, arguments ...string) (ExitCode, string) {
	return runAgentWith(reg, nil, arguments...)
}

// runAgentWith runs the agent as runArtifactAgent does, with options applied
// after ones that keep it from finding a kubelet on the machine running the
// tests.
func runAgentWith(reg *registrytest.Registry, options []Option, arguments ...string) (ExitCode, string) {
	all := []Option{WithProcRoot(GinkgoT().TempDir())}
	if reg != nil {
		all = append(all, WithRegistryHTTPClient(reg.Client()))
	}
	stderr := &bytes.Buffer{}
	exitCode := New(append(all, options...)...).Run(context.Background(), arguments, io.Discard, stderr)
	return exitCode, stderr.String()
}

func newArtifactRegistry(options registrytest.Options) *registrytest.Registry {
	reg := registrytest.New(options)
	DeferCleanup(reg.Close)
	return reg
}

// pushArtifact pushes spec to reg and returns its digest reference.
func pushArtifact(reg *registrytest.Registry, spec registrytest.PackageSpec) string {
	GinkgoHelper()
	desc, _, err := registrytest.PushPackage(
		context.Background(),
		reg.Repository(artifactTestRepository),
		"1.0.0",
		spec,
	)
	Expect(err).NotTo(HaveOccurred())
	return reg.Host() + "/" + artifactTestRepository + "@" + desc.Digest.String()
}

func supportedPackage(payload string) registrytest.PackageSpec {
	return registrytest.PackageSpec{
		Uninstall: artifact.UninstallSupported,
		Payload:   []registrytest.Layer{{Dir: payload}},
	}
}

// writeArtifactPayload writes a new payload directory whose apply-check
// records "payload", and returns it.
func writeArtifactPayload() string {
	GinkgoHelper()
	directory := GinkgoT().TempDir()
	writeArtifactPackage(directory, "payload")
	return directory
}

// writeArtifactPackage writes the package of writePackageFixture with on_host
// false steps that run: apply and second succeed, and apply-check writes
// checkMarker to the check file of the marker directory.
func writeArtifactPackage(directory, checkMarker string) {
	GinkgoHelper()
	writePackageFixture(directory, "[]", false)
	scripts := map[string]string{
		"apply":       "#!/bin/sh\n",
		"second":      "#!/bin/sh\n",
		"apply-check": fmt.Sprintf("#!/bin/sh\necho %s > \"$%s/check\"\n", checkMarker, artifactMarkerEnv),
	}
	for name, script := range scripts {
		Expect(os.WriteFile(filepath.Join(directory, "skyhook_dir", name), []byte(script), 0o700)).To(Succeed())
	}
}

// writeDockerConfig writes a Docker config whose one auths entry, for host,
// holds the test registry's credentials.
func writeDockerConfig(path, host string) {
	GinkgoHelper()
	auth := base64.StdEncoding.EncodeToString([]byte(artifactTestUsername + ":" + artifactTestPassword))
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(os.WriteFile(path, fmt.Appendf(nil, `{"auths":{%q:{"auth":%q}}}`, host, auth), 0o600)).To(Succeed())
}

func hostFilesLayers() []registrytest.HostFilesLayer {
	GinkgoHelper()
	directory := GinkgoT().TempDir()
	Expect(os.WriteFile(filepath.Join(directory, "host.conf"), []byte("host"), 0o644)).To(Succeed())
	return []registrytest.HostFilesLayer{{
		Layer:       registrytest.Layer{Dir: directory},
		Destination: "/opt/nodewright-test",
	}}
}

// interruptDeclaration declares steps with empty arguments: nil arguments
// marshal as null, which the metadata schema rejects.
func interruptDeclaration() *artifact.Interrupt {
	return &artifact.Interrupt{
		Sense: artifact.InterruptStep{Path: "sense", Arguments: []string{}},
		Run:   artifact.InterruptStep{Path: "run", Arguments: []string{}},
	}
}

func permissions(path string) fs.FileMode {
	GinkgoHelper()
	info, err := os.Stat(path)
	Expect(err).NotTo(HaveOccurred())
	return info.Mode().Perm()
}
