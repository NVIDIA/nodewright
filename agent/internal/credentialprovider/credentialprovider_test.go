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

package credentialprovider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nodewright/artifact/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// pluginHelperEnv makes this test binary a plugin that prints the file the
// variable names, for the chroot test to run.
const pluginHelperEnv = "NODEWRIGHT_TEST_PLUGIN_PRINTS"

func TestCredentialProvider(t *testing.T) {
	if path, ok := os.LookupEnv(pluginHelperEnv); ok {
		data, err := os.ReadFile(path)
		if err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.Write(data)
		os.Exit(0)
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Credential Provider Suite")
}

const (
	testConfigPath = "/etc/kubernetes/credential-provider.yaml"
	testBinDir     = "/opt/credential-provider/bin"
	testPassword   = "placeholder-password"
	testV1         = "credentialprovider.kubelet.k8s.io/v1"
)

var _ = Describe("flagValue", func() {
	DescribeTable("reads a flag as the kubelet parses it",
		func(want string, args ...string) {
			Expect(flagValue(append([]string{"/usr/bin/kubelet"}, args...), configFlag)).To(Equal(want))
		},
		Entry("with an inline value", "/a", "--image-credential-provider-config=/a"),
		Entry("with the value as the next argument", "/a", "--v=2", "--image-credential-provider-config", "/a", "--node-ip=10.0.0.1"),
		Entry("with underscores in the name", "/a", "--image_credential_provider_config=/a"),
		Entry("given twice, by the last", "/b", "--image-credential-provider-config=/a", "--image-credential-provider-config", "/b"),
		Entry("after a bare --, not at all", "", "--", "--image-credential-provider-config=/a"),
		Entry("with a longer name, not at all", "", "--image-credential-provider-config-dir=/a"),
		Entry("with a single dash, not at all", "", "-image-credential-provider-config=/a"),
	)
})

var _ = Describe("findKubelet", func() {
	It("finds the kubelet of lowest PID by argv[0], with its flags and environment", func() {
		procRoot := GinkgoT().TempDir()
		writeProcess(procRoot, "1", []string{"/sbin/init"}, nil)
		writeProcess(procRoot, "300", []string{"/usr/bin/kubelet", "--image-credential-provider-config=/other"}, []string{"PATH=/bin"})
		writeProcess(procRoot, "200", []string{
			"/usr/bin/kubelet",
			"--config=/var/lib/kubelet/config.yaml",
			"--image-credential-provider-config", testConfigPath,
			"--image-credential-provider-bin-dir=" + testBinDir,
		}, []string{"PATH=/usr/bin:/bin", "AWS_REGION=us-west-2"})
		writeProcess(procRoot, "100", []string{"/usr/local/bin/kubelet-wrapper", "--image-credential-provider-config=/wrapper"}, nil)
		Expect(os.Mkdir(filepath.Join(procRoot, "self"), 0o755)).To(Succeed())

		k, err := findKubelet(procRoot)

		Expect(err).NotTo(HaveOccurred())
		Expect(k).To(Equal(kubelet{
			pid:     200,
			environ: []string{"PATH=/usr/bin:/bin", "AWS_REGION=us-west-2"},
			config:  testConfigPath,
			binDir:  testBinDir,
		}))
	})

	It("finds no kubelet when none runs", func() {
		procRoot := GinkgoT().TempDir()
		writeProcess(procRoot, "1", []string{"/sbin/init"}, nil)

		_, err := findKubelet(procRoot)

		Expect(err).To(MatchError(ContainSubstring("no kubelet process in " + procRoot)))
	})

	DescribeTable("skips a kubelet of lower PID outside the namespaces of process 1, and finds the node's own",
		func(place func(procRoot, pid string)) {
			procRoot := GinkgoT().TempDir()
			writeProcess(procRoot, "1", []string{"/sbin/init"}, nil)
			writeProcess(procRoot, "50", []string{
				"/usr/bin/kubelet",
				"--image-credential-provider-config=/var/lib/kubelet/pods/uid/volumes/kubernetes.io~empty-dir/x/config.yaml",
				"--image-credential-provider-bin-dir=/var/lib/kubelet/pods/uid/volumes/kubernetes.io~empty-dir/x/bin",
			}, []string{"PATH=/tenant"})
			place(procRoot, "50")
			writeProcess(procRoot, "200", []string{
				"/usr/bin/kubelet",
				"--image-credential-provider-config=" + testConfigPath,
				"--image-credential-provider-bin-dir=" + testBinDir,
			}, []string{"PATH=/usr/bin:/bin"})

			k, err := findKubelet(procRoot)

			Expect(err).NotTo(HaveOccurred())
			Expect(k).To(Equal(kubelet{pid: 200, environ: []string{"PATH=/usr/bin:/bin"}, config: testConfigPath, binDir: testBinDir}))
		},
		Entry("in another mount namespace, as a container of a hostPID pod runs", func(procRoot, pid string) {
			setNamespaces(procRoot, pid, "mnt:[4026532999]", hostPIDNS)
		}),
		Entry("in another PID namespace", func(procRoot, pid string) {
			setNamespaces(procRoot, pid, hostMountNS, "pid:[4026532998]")
		}),
		Entry("whose namespaces cannot be read", func(procRoot, pid string) {
			Expect(os.RemoveAll(filepath.Join(procRoot, pid, "ns"))).To(Succeed())
		}),
	)

	It("finds no kubelet when the only one runs outside the namespaces of process 1", func() {
		procRoot := GinkgoT().TempDir()
		writeProcess(procRoot, "1", []string{"/sbin/init"}, nil)
		writeProcess(procRoot, "200", []string{"/usr/bin/kubelet", "--image-credential-provider-config=" + testConfigPath}, nil)
		setNamespaces(procRoot, "200", "mnt:[4026532999]", hostPIDNS)

		_, err := findKubelet(procRoot)

		Expect(err).To(MatchError(ContainSubstring("no kubelet process in " + procRoot)))
	})

	It("fails when the namespaces of process 1 cannot be read", func() {
		procRoot := GinkgoT().TempDir()
		writeProcess(procRoot, "200", []string{"/usr/bin/kubelet", "--image-credential-provider-config=" + testConfigPath}, nil)

		_, err := findKubelet(procRoot)

		Expect(err).To(MatchError(ContainSubstring("namespaces of process 1")))
	})
})

var _ = Describe("readConfig", func() {
	yamlConfig := func(apiVersion string) string {
		return `apiVersion: ` + apiVersion + `
kind: CredentialProviderConfig
providers:
  - name: ecr-credential-provider
    matchImages:
      - "*.dkr.ecr.*.amazonaws.com"
    defaultCacheDuration: "12h"
    apiVersion: credentialprovider.kubelet.k8s.io/v1
    args:
      - get-credentials
    env:
      - name: AWS_PROFILE
        value: node
`
	}
	jsonConfig := func(apiVersion string) string {
		return "{\n\t\"apiVersion\": \"" + apiVersion + `",
	"kind": "CredentialProviderConfig",
	"providers": [{
		"name": "ecr-credential-provider",
		"matchImages": ["*.dkr.ecr.*.amazonaws.com"],
		"defaultCacheDuration": "12h",
		"apiVersion": "credentialprovider.kubelet.k8s.io/v1",
		"args": ["get-credentials"],
		"env": [{"name": "AWS_PROFILE", "value": "node"}]
	}]
}`
	}

	write := func(dir, name, content string) string {
		GinkgoHelper()
		path := filepath.Join(dir, name)
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		return path
	}

	DescribeTable("reads a config in YAML or JSON, of each version the kubelet reads",
		func(format func(string) string, apiVersion string) {
			providers, err := readConfig(write(GinkgoT().TempDir(), "config", format(apiVersion)))

			Expect(err).NotTo(HaveOccurred())
			Expect(providers).To(HaveLen(1))
			p := providers[0]
			Expect(p.Name).To(Equal("ecr-credential-provider"))
			Expect(p.MatchImages).To(Equal([]string{"*.dkr.ecr.*.amazonaws.com"}))
			Expect(p.APIVersion).To(Equal(testV1))
			Expect(p.Args).To(Equal([]string{"get-credentials"}))
			Expect(p.Env).To(HaveLen(1))
			Expect(p.Env[0].Name + "=" + p.Env[0].Value).To(Equal("AWS_PROFILE=node"))
			Expect(p.TokenAttributes).To(BeNil())
		},
		Entry("YAML, v1", yamlConfig, "kubelet.config.k8s.io/v1"),
		Entry("YAML, v1beta1", yamlConfig, "kubelet.config.k8s.io/v1beta1"),
		Entry("YAML, v1alpha1", yamlConfig, "kubelet.config.k8s.io/v1alpha1"),
		Entry("JSON, v1", jsonConfig, "kubelet.config.k8s.io/v1"),
		Entry("JSON, v1beta1", jsonConfig, "kubelet.config.k8s.io/v1beta1"),
		Entry("JSON, v1alpha1", jsonConfig, "kubelet.config.k8s.io/v1alpha1"),
	)

	DescribeTable("refuses what the kubelet would not read",
		func(content, want string) {
			_, err := readConfig(write(GinkgoT().TempDir(), "config.yaml", content))

			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(err.Error()).NotTo(ContainSubstring(testPassword))
		},
		Entry("another kind", "apiVersion: kubelet.config.k8s.io/v1\nkind: KubeletConfiguration\n", `kind "KubeletConfiguration"`),
		Entry("another group", "apiVersion: example.com/v1\nkind: CredentialProviderConfig\n", `apiVersion "example.com/v1"`),
		Entry("a version the kubelet does not read", "apiVersion: kubelet.config.k8s.io/v2\nkind: CredentialProviderConfig\n", `apiVersion "kubelet.config.k8s.io/v2"`),
		Entry("malformed YAML, without quoting it", "providers: [{name: a, env: "+testPassword+"}]\n", "is malformed"),
	)

	It("merges a directory's .json, .yaml and .yml files in lexical order", func() {
		dir := GinkgoT().TempDir()
		provider := func(name string) string {
			return "apiVersion: kubelet.config.k8s.io/v1\nkind: CredentialProviderConfig\nproviders:\n  - name: " + name + "\n"
		}
		write(dir, "b.yml", provider("b"))
		write(dir, "a.json", `{"apiVersion":"kubelet.config.k8s.io/v1","kind":"CredentialProviderConfig","providers":[{"name":"a"}]}`)
		write(dir, "c.yaml", provider("c"))
		write(dir, "d.txt", provider("d"))
		Expect(os.Mkdir(filepath.Join(dir, "e.yaml"), 0o755)).To(Succeed())

		providers, err := readConfig(dir)

		Expect(err).NotTo(HaveOccurred())
		var names []string
		for _, p := range providers {
			names = append(names, p.Name)
		}
		Expect(names).To(Equal([]string{"a", "b", "c"}))
	})

	It("refuses a provider named twice across a directory's files", func() {
		dir := GinkgoT().TempDir()
		for _, file := range []string{"a.yaml", "b.yaml"} {
			write(dir, file, "apiVersion: kubelet.config.k8s.io/v1\nkind: CredentialProviderConfig\nproviders:\n  - name: same\n")
		}

		_, err := readConfig(dir)

		Expect(err).To(MatchError(ContainSubstring(`provider "same" twice`)))
	})
})

var _ = Describe("Sources", func() {
	var (
		root     string
		procRoot string
		ref      client.Reference
		commands []Command
		answers  map[string]func(Command) ([]byte, error)
		host     Host
	)

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		procRoot = GinkgoT().TempDir()
		writeProcess(procRoot, "1", []string{"/sbin/init"}, nil)
		writeProcess(procRoot, "42", []string{
			"/usr/bin/kubelet",
			"--image-credential-provider-config=" + testConfigPath,
			"--image-credential-provider-bin-dir", testBinDir,
		}, []string{"PATH=/usr/bin:/bin", "KUBELET_ONLY=kubelet"})
		var err error
		ref, err = client.ParseReference("registry.example.com/org/app:1.0")
		Expect(err).NotTo(HaveOccurred())
		commands = nil
		answers = map[string]func(Command) ([]byte, error){}
		host = Host{RootMount: root, ProcRoot: procRoot, Runner: func(ctx context.Context, cmd Command) ([]byte, error) {
			deadline, ok := ctx.Deadline()
			Expect(ok).To(BeTrue())
			Expect(time.Until(deadline)).To(BeNumerically("<=", execTimeout))
			commands = append(commands, cmd)
			answer, ok := answers[cmd.Path]
			Expect(ok).To(BeTrue(), "no answer for "+cmd.Path)
			return answer(cmd)
		}}
	})

	writeConfig := func(providers string) {
		GinkgoHelper()
		path := filepath.Join(root, testConfigPath)
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte("apiVersion: kubelet.config.k8s.io/v1\nkind: CredentialProviderConfig\nproviders:\n"+providers), 0o600)).To(Succeed())
	}
	providerEntry := func(name, apiVersion, matchImage string, extra ...string) string {
		return fmt.Sprintf("  - name: %s\n    apiVersion: %s\n    matchImages: [%q]\n    defaultCacheDuration: 1m\n%s",
			name, apiVersion, matchImage, strings.Join(extra, ""))
	}
	answer := func(name string, stdout string) {
		answers[filepath.Join(testBinDir, name)] = func(Command) ([]byte, error) { return []byte(stdout), nil }
	}
	response := func(apiVersion, host string) string {
		return fmt.Sprintf(`{"apiVersion":%q,"kind":"CredentialProviderResponse","cacheKeyType":"Registry","cacheDuration":"5m",`+
			`"auth":{%q:{"username":"user","password":%q}}}`, apiVersion, host, testPassword)
	}
	labels := func(sources []client.CredentialSource) []string {
		creds, _ := client.LookupCredential(ref, sources)
		var found []string
		for _, cred := range creds {
			found = append(found, cred.Source())
		}
		return found
	}

	It("runs each plugin that matches with the kubelet's environment, its configured args and env, and a request for the image name", func() {
		writeConfig(providerEntry("match", testV1, "*.example.com",
			"    args: [get-credentials, --verbose]\n    env:\n      - {name: PLUGIN_ENV, value: plugin}\n      - {name: PATH, value: /opt/bin}\n") +
			providerEntry("other", testV1, "*.other.io"))
		answer("match", response(testV1, "registry.example.com"))

		sources, skipped := Sources(context.Background(), ref, host)

		Expect(commands).To(HaveLen(1))
		Expect(commands[0].Path).To(Equal(filepath.Join(testBinDir, "match")))
		Expect(commands[0].Args).To(Equal([]string{"get-credentials", "--verbose"}))
		Expect(commands[0].Env).To(Equal([]string{"PATH=/usr/bin:/bin", "KUBELET_ONLY=kubelet", "PLUGIN_ENV=plugin", "PATH=/opt/bin"}))
		Expect(commands[0].Stdin).To(MatchJSON(`{"apiVersion":"` + testV1 + `","kind":"CredentialProviderRequest","image":"registry.example.com/org/app"}`))
		Expect(labels(sources)).To(Equal([]string{"credential provider match"}))
		Expect(skipped).To(Equal([]client.SkippedSource{{
			Source: "credential provider other",
			Reason: "no matchImages entry matches registry.example.com/org/app",
		}}))
	})

	DescribeTable("talks to a plugin in its configured apiVersion",
		func(apiVersion string) {
			writeConfig(providerEntry("plugin", apiVersion, "registry.example.com"))
			answer("plugin", response(apiVersion, "registry.example.com"))

			sources, skipped := Sources(context.Background(), ref, host)

			Expect(skipped).To(BeEmpty())
			Expect(commands[0].Stdin).To(MatchJSON(`{"apiVersion":"` + apiVersion + `","kind":"CredentialProviderRequest","image":"registry.example.com/org/app"}`))
			Expect(labels(sources)).To(Equal([]string{"credential provider plugin"}))
		},
		Entry("v1", testV1),
		Entry("v1beta1", "credentialprovider.kubelet.k8s.io/v1beta1"),
		Entry("v1alpha1", "credentialprovider.kubelet.k8s.io/v1alpha1"),
	)

	It("asks Docker Hub plugins about the normalised image name, as the kubelet does", func() {
		writeConfig(providerEntry("hub", testV1, "docker.io"))
		answer("hub", response(testV1, "docker.io"))
		var err error
		ref, err = client.ParseReference("alpine:3.20")
		Expect(err).NotTo(HaveOccurred())

		sources, _ := Sources(context.Background(), ref, host)

		Expect(commands[0].Stdin).To(MatchJSON(`{"apiVersion":"` + testV1 + `","kind":"CredentialProviderRequest","image":"docker.io/library/alpine"}`))
		Expect(labels(sources)).To(Equal([]string{"credential provider hub"}))
	})

	DescribeTable("skips a provider it does not run",
		func(entry string, want string) {
			writeConfig(entry)

			sources, skipped := Sources(context.Background(), ref, host)

			Expect(sources).To(BeEmpty())
			Expect(commands).To(BeEmpty())
			Expect(skipped).To(HaveLen(1))
			Expect(skipped[0].Reason).To(ContainSubstring(want))
		},
		Entry("one that asks for a service account token", providerEntry("token", testV1, "registry.example.com",
			"    tokenAttributes:\n      serviceAccountTokenAudience: registry\n      requireServiceAccount: true\n      cacheType: ServiceAccount\n"),
			"tokenAttributes"),
		Entry("one of an API version the kubelet does not speak", providerEntry("future", "credentialprovider.kubelet.k8s.io/v2", "registry.example.com"),
			`apiVersion "credentialprovider.kubelet.k8s.io/v2"`),
		Entry("one whose name is not a file name", providerEntry("../escape", testV1, "registry.example.com"), "single path component"),
	)

	DescribeTable("skips a response the kubelet refuses, quoting none of it",
		func(stdout string, want string) {
			writeConfig(providerEntry("plugin", testV1, "registry.example.com"))
			answer("plugin", stdout)

			sources, skipped := Sources(context.Background(), ref, host)

			Expect(sources).To(BeEmpty())
			Expect(skipped).To(HaveLen(1))
			Expect(skipped[0].Source).To(Equal("credential provider plugin"))
			Expect(skipped[0].Reason).To(ContainSubstring(want))
			Expect(skipped[0].Reason).NotTo(ContainSubstring(testPassword))
		},
		Entry("output that is not JSON", "password="+testPassword, "not a CredentialProviderResponse"),
		Entry("an unknown field", strings.Replace(response(testV1, "registry.example.com"), `"auth"`, `"extra":1,"auth"`, 1), "not a CredentialProviderResponse"),
		Entry("trailing output", response(testV1, "registry.example.com")+"{}", "followed by more output"),
		Entry("another apiVersion", response("credentialprovider.kubelet.k8s.io/v1beta1", "registry.example.com"), "is not the configured"),
		Entry("another kind", strings.Replace(response(testV1, "registry.example.com"), "CredentialProviderResponse", "CredentialProviderRequest", 1), "kind"),
		Entry("no cacheKeyType", strings.Replace(response(testV1, "registry.example.com"), `"cacheKeyType":"Registry",`, "", 1), "cacheKeyType"),
		Entry("a cacheDuration that is not a duration", strings.Replace(response(testV1, "registry.example.com"), `"5m"`, `"soon"`, 1), "cacheDuration"),
	)

	It("goes on to the next plugin after one that fails", func() {
		writeConfig(providerEntry("first", testV1, "registry.example.com") + providerEntry("second", testV1, "registry.example.com"))
		answers[filepath.Join(testBinDir, "first")] = func(Command) ([]byte, error) { return nil, errors.New("exit status 1: denied") }
		answer("second", response(testV1, "registry.example.com"))

		sources, skipped := Sources(context.Background(), ref, host)

		Expect(labels(sources)).To(Equal([]string{"credential provider second"}))
		Expect(skipped).To(Equal([]client.SkippedSource{{Source: "credential provider first", Reason: "exit status 1: denied"}}))
	})

	It("keeps the order of the configured plugins", func() {
		writeConfig(providerEntry("first", testV1, "registry.example.com") + providerEntry("second", testV1, "registry.example.com"))
		answer("first", response(testV1, "registry.example.com"))
		answer("second", response(testV1, "registry.example.com/org"))

		sources, _ := Sources(context.Background(), ref, host)

		Expect(labels(sources)).To(Equal([]string{"credential provider first", "credential provider second"}))
	})

	DescribeTable("says why no plugin runs",
		func(prepare func(), want string) {
			writeConfig(providerEntry("plugin", testV1, "registry.example.com"))
			prepare()

			sources, skipped := Sources(context.Background(), ref, host)

			Expect(sources).To(BeEmpty())
			Expect(commands).To(BeEmpty())
			Expect(skipped).To(HaveLen(1))
			Expect(skipped[0].Source).To(Equal(SourceName))
			Expect(skipped[0].Reason).To(ContainSubstring(want))
		},
		Entry("no kubelet", func() {
			host.ProcRoot = GinkgoT().TempDir()
			writeProcess(host.ProcRoot, "1", []string{"/sbin/init"}, nil)
		}, "no kubelet process"),
		Entry("a kubelet that configures no plugins", func() {
			writeProcess(procRoot, "42", []string{"/usr/bin/kubelet", "--config=/var/lib/kubelet/config.yaml"}, nil)
		}, "does not set both --image-credential-provider-config and --image-credential-provider-bin-dir"),
		Entry("a relative plugin directory", func() {
			writeProcess(procRoot, "42", []string{"kubelet", "--image-credential-provider-config=" + testConfigPath, "--image-credential-provider-bin-dir=bin"}, nil)
		}, "is not absolute"),
		Entry("a missing config", func() {
			Expect(os.Remove(filepath.Join(root, testConfigPath))).To(Succeed())
		}, "no such file or directory"),
		Entry("a config under the kubelet's pod directory", func() {
			writeProcess(procRoot, "42", []string{"/usr/bin/kubelet",
				"--image-credential-provider-config=/var/lib/kubelet/pods/uid/volumes/kubernetes.io~empty-dir/x/config.yaml",
				"--image-credential-provider-bin-dir=" + testBinDir}, nil)
		}, "lies under the kubelet's pod directory /var/lib/kubelet/pods"),
		Entry("a plugin directory under the kubelet's pod directory", func() {
			writeProcess(procRoot, "42", []string{"/usr/bin/kubelet",
				"--image-credential-provider-config=" + testConfigPath,
				"--image-credential-provider-bin-dir=/var/lib/kubelet/pods/uid/volumes/kubernetes.io~empty-dir/x/bin"}, nil)
		}, "lies under the kubelet's pod directory /var/lib/kubelet/pods"),
		Entry("a plugin directory that is the pod directory of a kubelet with its own --root-dir", func() {
			writeProcess(procRoot, "42", []string{"/usr/bin/kubelet", "--root-dir", "/data/kubelet",
				"--image-credential-provider-config=" + testConfigPath,
				"--image-credential-provider-bin-dir=/data/kubelet/pods/"}, nil)
		}, "lies under the kubelet's pod directory /data/kubelet/pods"),
	)

	It("runs plugins configured beside the kubelet's pod directory", func() {
		writeProcess(procRoot, "42", []string{"/usr/bin/kubelet",
			"--image-credential-provider-config=" + testConfigPath,
			"--image-credential-provider-bin-dir=" + testBinDir,
			"--root-dir=/opt/credential-provider"}, nil)
		writeConfig(providerEntry("plugin", testV1, "registry.example.com"))
		answer("plugin", response(testV1, "registry.example.com"))

		sources, skipped := Sources(context.Background(), ref, host)

		Expect(skipped).To(BeEmpty())
		Expect(labels(sources)).To(Equal([]string{"credential provider plugin"}))
	})
})

var _ = Describe("ExecRunner", func() {
	writeScript := func(script string) string {
		GinkgoHelper()
		path := filepath.Join(GinkgoT().TempDir(), "plugin")
		Expect(os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700)).To(Succeed())
		return path
	}

	It("runs a plugin with exactly its arguments, environment and standard input", func() {
		GinkgoT().Setenv("NODEWRIGHT_AGENT_ONLY", "agent")
		plugin := writeScript(`cat
printf 'args=%s\n' "$*"
printf 'plugin=%s\n' "$PLUGIN_ENV"
printf 'agent=%s\n' "$NODEWRIGHT_AGENT_ONLY"
printf 'pwd=%s\n' "$(pwd)"
`)

		stdout, err := ExecRunner("")(context.Background(), Command{
			Path:  plugin,
			Args:  []string{"get-credentials", "--verbose"},
			Env:   []string{"PATH=/usr/bin:/bin", "PLUGIN_ENV=plugin"},
			Stdin: []byte("request\n"),
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(string(stdout)).To(Equal("request\nargs=get-credentials --verbose\nplugin=plugin\nagent=\npwd=/\n"))
	})

	It("reports a failed plugin with its standard error and without its standard output", func() {
		plugin := writeScript("echo " + testPassword + "\necho denied >&2\nexit 3\n")

		_, err := ExecRunner("")(context.Background(), Command{Path: plugin})

		Expect(err).To(MatchError(ContainSubstring("exit status 3: denied")))
		Expect(err.Error()).NotTo(ContainSubstring(testPassword))
	})

	It("kills a plugin when its context ends", func() {
		plugin := writeScript("exec sleep 30\n")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		DeferCleanup(cancel)
		started := time.Now()

		_, err := ExecRunner("")(ctx, Command{Path: plugin, Env: []string{"PATH=/usr/bin:/bin"}})

		Expect(err).To(MatchError(context.DeadlineExceeded))
		Expect(time.Since(started)).To(BeNumerically("<", 10*time.Second))
	})
})

// The mount and PID namespaces of every process writeProcess writes, as the
// links of /proc/<pid>/ns name them.
const (
	hostMountNS = "mnt:[4026531841]"
	hostPIDNS   = "pid:[4026531836]"
)

// setNamespaces points the ns/mnt and ns/pid links of process pid beneath
// procRoot at mnt and pidNS.
func setNamespaces(procRoot, pid, mnt, pidNS string) {
	GinkgoHelper()
	dir := filepath.Join(procRoot, pid, "ns")
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	for name, target := range map[string]string{"mnt": mnt, "pid": pidNS} {
		link := filepath.Join(dir, name)
		Expect(os.RemoveAll(link)).To(Succeed())
		Expect(os.Symlink(target, link)).To(Succeed())
	}
}

// writeProcess writes the cmdline, environ and namespaces of process pid
// beneath procRoot, as /proc shows them, in hostMountNS and hostPIDNS.
func writeProcess(procRoot, pid string, argv, environ []string) {
	GinkgoHelper()
	dir := filepath.Join(procRoot, pid)
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	nul := func(fields []string) []byte {
		if len(fields) == 0 {
			return nil
		}
		return []byte(strings.Join(fields, "\x00") + "\x00")
	}
	Expect(os.WriteFile(filepath.Join(dir, "cmdline"), nul(argv), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "environ"), nul(environ), 0o600)).To(Succeed())
	setNamespaces(procRoot, pid, hostMountNS, hostPIDNS)
}
