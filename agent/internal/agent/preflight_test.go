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

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nodewright/agent/internal/credentialprovider"
	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/preflight"
	"github.com/NVIDIA/nodewright/artifact/registrytest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	fakePluginName    = "fake-credential-provider"
	fakeKubeletPID    = "4242"
	kubeletConfigPath = "/etc/kubernetes/credential-provider.yaml"
	kubeletPluginDir  = "/usr/libexec/kubernetes/kubelet-plugins/credential-provider/exec"
)

var _ = Describe("preflight", func() {
	var (
		root       string
		resultFile string
	)

	BeforeEach(func() {
		unsetEnvironment(preflightRefEnv)
		unsetEnvironment(registryAuthFilesEnv)
		unsetEnvironment(resourceIDEnv)
		root = GinkgoT().TempDir()
		resultFile = filepath.Join(GinkgoT().TempDir(), "termination-log")
	})

	runPreflight := func(reg *registrytest.Registry, options ...Option) (ExitCode, string) {
		options = append([]Option{WithPreflightResultFile(resultFile)}, options...)
		return runAgentWith(reg, options, preflightMode, root)
	}

	// preflightOf runs a preflight of reference and returns the result as the
	// operator decodes it, with the agent's stderr.
	preflightOf := func(reg *registrytest.Registry, reference string, options ...Option) (preflight.Result, string) {
		GinkgoHelper()
		GinkgoT().Setenv(preflightRefEnv, reference)
		exitCode, stderr := runPreflight(reg, options...)
		Expect(exitCode).To(Equal(ExitSuccess), stderr)
		data, err := os.ReadFile(resultFile)
		Expect(err).NotTo(HaveOccurred())
		result, err := preflight.Decode(data, reference)
		Expect(err).NotTo(HaveOccurred(), string(data))
		return result, stderr
	}

	tagOf := func(reg *registrytest.Registry, tag string) string {
		return reg.Host() + "/" + artifactTestRepository + ":" + tag
	}

	It("reports an artifact by the digest its tag resolves to, without touching the host", func() {
		reg := newArtifactRegistry(registrytest.Options{})
		pinned := pushArtifact(reg, supportedPackage(writeArtifactPayload()))

		result, stderr := preflightOf(reg, tagOf(reg, "1.0.0"))

		Expect(result.Outcome).To(Equal(preflight.Outcome{
			Kind:      preflight.KindArtifact,
			Pinned:    pinned,
			Uninstall: artifact.UninstallSupported,
		}))
		Expect(result.Credential).To(BeEmpty())
		Expect(stderr).To(ContainSubstring("preflight result"))
		Expect(os.ReadDir(root)).To(BeEmpty())
	})

	It("reports an artifact that declares uninstall unsupported", func() {
		reg := newArtifactRegistry(registrytest.Options{})
		spec := supportedPackage(writeArtifactPayload())
		spec.Uninstall = artifact.UninstallUnsupported
		pinned := pushArtifact(reg, spec)

		result, _ := preflightOf(reg, pinned)

		Expect(result.Outcome).To(Equal(preflight.Outcome{
			Kind:      preflight.KindArtifact,
			Pinned:    pinned,
			Uninstall: artifact.UninstallUnsupported,
		}))
	})

	It("reports a container image", func() {
		reg := newArtifactRegistry(registrytest.Options{})
		desc, err := registrytest.PushImage(context.Background(), reg.Repository(artifactTestRepository), "image", writeArtifactPayload())
		Expect(err).NotTo(HaveOccurred())

		result, _ := preflightOf(reg, tagOf(reg, "image"))

		Expect(result.Outcome).To(Equal(preflight.Outcome{
			Kind:   preflight.KindImage,
			Pinned: reg.Host() + "/" + artifactTestRepository + "@" + desc.Digest.String(),
		}))
	})

	DescribeTable(
		"reports an artifact this version rejects, with the reason",
		func(change func(*registrytest.PackageSpec), reason string) {
			reg := newArtifactRegistry(registrytest.Options{})
			spec := supportedPackage(writeArtifactPayload())
			change(&spec)
			pinned := pushArtifact(reg, spec)

			result, _ := preflightOf(reg, pinned)

			Expect(result.Outcome).To(Equal(preflight.Outcome{Kind: preflight.KindRejected, Reason: reason}))
		},
		Entry("an interrupt", func(spec *registrytest.PackageSpec) {
			spec.Interrupt = interruptDeclaration()
		}, preflight.ReasonInterrupt),
		Entry("host-files layers", func(spec *registrytest.PackageSpec) {
			spec.HostFiles = hostFilesLayers()
		}, preflight.ReasonHostFiles),
		Entry("metadata outside the schema", func(spec *registrytest.PackageSpec) {
			spec.Uninstall = "optional"
		}, preflight.ReasonInvalidArtifact+string(artifact.ReasonMetadataSchema)),
	)

	It("reports a reference the registry does not have", func() {
		reg := newArtifactRegistry(registrytest.Options{})

		result, _ := preflightOf(reg, tagOf(reg, "missing"))

		Expect(result.Outcome).To(Equal(preflight.Outcome{Kind: preflight.KindUnknown, ErrorClass: preflight.ErrorNotFound}))
		Expect(result.Detail).To(ContainSubstring("missing"))
	})

	Describe("registry credentials", func() {
		var (
			reg       *registrytest.Registry
			reference string
		)

		BeforeEach(func() {
			reg = newArtifactRegistry(registrytest.Options{
				Username: artifactTestUsername,
				Password: artifactTestPassword,
			})
			pushArtifact(reg, supportedPackage(writeArtifactPayload()))
			reference = tagOf(reg, "1.0.0")
		})

		expectNoSecret := func(output ...string) {
			GinkgoHelper()
			authValue := base64.StdEncoding.EncodeToString([]byte(artifactTestUsername + ":" + artifactTestPassword))
			for _, text := range output {
				Expect(text).NotTo(ContainSubstring(artifactTestPassword))
				Expect(text).NotTo(ContainSubstring(authValue))
			}
		}

		resultText := func() string {
			GinkgoHelper()
			data, err := os.ReadFile(resultFile)
			Expect(err).NotTo(HaveOccurred())
			return string(data)
		}

		It("reports unauthorized when no source holds a credential", func() {
			result, stderr := preflightOf(reg, reference)

			Expect(result.Outcome).To(Equal(preflight.Outcome{Kind: preflight.KindUnknown, ErrorClass: preflight.ErrorUnauthorized}))
			Expect(stderr).To(ContainSubstring("reading the package artifact anonymously"))
		})

		It("authenticates with an auth file of the list, skipping a missing one, and reports which", func() {
			missing := filepath.Join(GinkgoT().TempDir(), "missing", ".dockerconfigjson")
			authFile := filepath.Join(GinkgoT().TempDir(), ".dockerconfigjson")
			writeDockerConfig(authFile, reg.Host())
			GinkgoT().Setenv(registryAuthFilesEnv, missing+string(filepath.ListSeparator)+authFile)

			result, stderr := preflightOf(reg, reference)

			Expect(result.Kind).To(Equal(preflight.KindArtifact))
			Expect(result.Credential).To(Equal(authFile))
			Expect(stderr).To(ContainSubstring("registry credential source skipped"))
			Expect(stderr).To(ContainSubstring(missing))
			Expect(stderr).To(ContainSubstring("file does not exist"))
			expectNoSecret(stderr, resultText())
		})

		It("authenticates with a kubelet credential provider plugin", func() {
			procRoot := writeFakeKubelet(root, fakeProviderConfig(reg.Host()))
			writeFakePlugin(root, fakeResponse(reg.Host()))

			result, stderr := preflightOf(reg, reference, WithProcRoot(procRoot), WithCredentialProviderRunner(fakePluginRunner(root)))

			Expect(result.Kind).To(Equal(preflight.KindArtifact))
			Expect(result.Credential).To(Equal("credential provider " + fakePluginName))
			expectNoSecret(stderr, resultText())
		})

		It("goes on without a plugin that fails", func() {
			procRoot := writeFakeKubelet(root, fakeProviderConfig(reg.Host()))
			writeFakePluginScript(root, "#!/bin/sh\necho denied >&2\nexit 1\n")
			authFile := filepath.Join(GinkgoT().TempDir(), ".dockerconfigjson")
			writeDockerConfig(authFile, reg.Host())
			GinkgoT().Setenv(registryAuthFilesEnv, authFile)

			result, stderr := preflightOf(reg, reference, WithProcRoot(procRoot), WithCredentialProviderRunner(fakePluginRunner(root)))

			Expect(result.Kind).To(Equal(preflight.KindArtifact))
			Expect(result.Credential).To(Equal(authFile))
			Expect(stderr).To(ContainSubstring("credential provider " + fakePluginName))
			Expect(stderr).To(ContainSubstring("denied"))
		})
	})

	DescribeTable(
		"reports a reference that does not parse as invalid, echoing it as given",
		func(reference string) {
			result, _ := preflightOf(nil, reference)

			Expect(result.Reference).To(Equal(reference))
			Expect(result.Outcome).To(Equal(preflight.Outcome{Kind: preflight.KindUnknown, ErrorClass: preflight.ErrorInvalidReference}))
			Expect(result.Detail).To(ContainSubstring(preflightRefEnv))
		},
		Entry("an empty reference", ""),
		Entry("a reference without a tag or digest", "registry.example.com/org/app"),
		Entry("a reference that is not an image", "not a reference"),
	)

	It("reports a preflight stopped by a termination signal as canceled", func() {
		GinkgoT().Setenv(preflightRefEnv, "registry.example.com/org/app:1.0")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stderr := &bytes.Buffer{}

		exitCode := New(WithPreflightResultFile(resultFile), WithProcRoot(GinkgoT().TempDir())).Run(
			ctx, []string{preflightMode, root}, io.Discard, stderr,
		)

		Expect(exitCode).To(Equal(ExitSuccess), stderr.String())
		data, err := os.ReadFile(resultFile)
		Expect(err).NotTo(HaveOccurred())
		result, err := preflight.Decode(data, "registry.example.com/org/app:1.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Outcome).To(Equal(preflight.Outcome{Kind: preflight.KindUnknown, ErrorClass: preflight.ErrorCanceled}))
	})

	DescribeTable(
		"rejects other arguments as a usage error",
		func(arguments ...string) {
			exitCode, stderr := runAgentWith(nil, []Option{WithPreflightResultFile(resultFile)}, arguments...)

			Expect(exitCode).To(Equal(ExitUsage))
			Expect(stderr).To(ContainSubstring(usage))
			Expect(resultFile).NotTo(BeAnExistingFile())
		},
		Entry("no root mount", preflightMode),
		Entry("a relative root mount", preflightMode, "root"),
		Entry("a copy directory", preflightMode, "/root", "/package"),
	)

	It("fails when it cannot write the result", func() {
		resultFile = filepath.Join(GinkgoT().TempDir(), "missing", "termination-log")

		exitCode, stderr := runPreflight(nil)

		Expect(exitCode).To(Equal(ExitFailure))
		Expect(stderr).To(ContainSubstring("writing the preflight result"))
	})

	It("fails when the result does not fit a termination message", func() {
		GinkgoT().Setenv(preflightRefEnv, "registry.example.com/"+strings.Repeat("a", preflight.MaxResultSize)+":1.0")

		exitCode, stderr := runPreflight(nil)

		Expect(exitCode).To(Equal(ExitFailure))
		Expect(stderr).To(ContainSubstring("encoding the preflight result"))
		Expect(resultFile).NotTo(BeAnExistingFile())
	})
})

// writeFakeKubelet writes config, a CredentialProviderConfig, beneath root at
// kubeletConfigPath, and returns a process root whose one kubelet names it
// and kubeletPluginDir in either flag form.
func writeFakeKubelet(root, config string) string {
	GinkgoHelper()
	configFile := filepath.Join(root, kubeletConfigPath)
	Expect(os.MkdirAll(filepath.Dir(configFile), 0o755)).To(Succeed())
	Expect(os.WriteFile(configFile, []byte(config), 0o600)).To(Succeed())

	procRoot := GinkgoT().TempDir()
	process := filepath.Join(procRoot, fakeKubeletPID)
	Expect(os.Mkdir(process, 0o755)).To(Succeed())
	argv := []string{
		"/usr/bin/kubelet",
		"--image-credential-provider-config=" + kubeletConfigPath,
		"--image-credential-provider-bin-dir", kubeletPluginDir,
	}
	Expect(os.WriteFile(filepath.Join(process, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(process, "environ"), []byte("PATH=/usr/bin:/bin\x00"), 0o600)).To(Succeed())
	return procRoot
}

// fakeProviderConfig configures fakePluginName for images matching
// matchImage.
func fakeProviderConfig(matchImage string) string {
	return fmt.Sprintf(`apiVersion: kubelet.config.k8s.io/v1
kind: CredentialProviderConfig
providers:
  - name: %s
    matchImages:
      - %q
    defaultCacheDuration: 1m
    apiVersion: credentialprovider.kubelet.k8s.io/v1
`, fakePluginName, matchImage)
}

// fakeResponse grants the test registry's credentials for host.
func fakeResponse(host string) string {
	return fmt.Sprintf(`{"apiVersion":"credentialprovider.kubelet.k8s.io/v1","kind":"CredentialProviderResponse",`+
		`"cacheKeyType":"Registry","auth":{%q:{"username":%q,"password":%q}}}`,
		host, artifactTestUsername, artifactTestPassword)
}

// writeFakePlugin writes fakePluginName beneath root, answering every request
// with response.
func writeFakePlugin(root, response string) {
	GinkgoHelper()
	writeFakePluginScript(root, "#!/bin/sh\ncat > /dev/null\ncat <<'EOF'\n"+response+"\nEOF\n")
}

func writeFakePluginScript(root, script string) {
	GinkgoHelper()
	plugin := filepath.Join(root, kubeletPluginDir, fakePluginName)
	Expect(os.MkdirAll(filepath.Dir(plugin), 0o755)).To(Succeed())
	Expect(os.WriteFile(plugin, []byte(script), 0o700)).To(Succeed())
}

// fakePluginRunner runs a plugin at its host path beneath root, without the
// chroot the tests cannot make.
func fakePluginRunner(root string) credentialprovider.Runner {
	run := credentialprovider.ExecRunner("")
	return func(ctx context.Context, cmd credentialprovider.Command) ([]byte, error) {
		cmd.Path = filepath.Join(root, cmd.Path)
		return run(ctx, cmd)
	}
}
