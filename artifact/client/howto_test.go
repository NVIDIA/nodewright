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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
)

const (
	howtoDocPath = "../../docs/user-guide/package-artifacts.md"
	// howtoRequireTools turns a tool missing from PATH from a skip into a
	// failure, so CI cannot pass by running none of the page's commands.
	howtoRequireTools = "ARTIFACT_REQUIRE_DOC_TOOLS"
	howtoTimeout      = 2 * time.Minute
	// howtoName names the package a layout is read as; only the tag of the
	// reference is resolved in a layout.
	howtoName = "registry.example.com/nodewright/hello"
)

// howtoMarkers are the markers of the page's executed blocks, each fenced
// with the info string "sh <marker>".
var howtoMarkers = []string{"payload", "metadata", "push", "push-layout", "convert"}

// howtoVariables are the variables the page's executed blocks read, and so
// the only ones the blocks may reference: ARTIFACT_REF only in push.
var howtoVariables = []string{"PACKAGE_DIR", "UNINSTALL", "IMAGE_REF", "ARTIFACT_REF"}

// howtoVarName matches the name of a variable reference after its $.
var howtoVarName = regexp.MustCompile(`^\{?([A-Za-z_][A-Za-z0-9_]*)`)

// howtoParse returns the body of each block of doc marked "sh <marker>", by
// marker. Like the specification page's examples, any fence info beyond a
// bare language must be a marker, so a mistyped one fails rather than
// silently dropping a block from the test.
func howtoParse(doc []byte) (map[string]string, error) {
	blocks := map[string]string{}
	var marker string
	var body strings.Builder
	inFence := false
	for i, line := range strings.Split(string(doc), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inFence && trimmed == "```":
			if marker != "" {
				if _, ok := blocks[marker]; ok {
					return nil, fmt.Errorf("line %d: a second block is marked %q", i+1, marker)
				}
				if err := howtoCheckVariables(marker, body.String()); err != nil {
					return nil, fmt.Errorf("line %d: %w", i+1, err)
				}
				blocks[marker] = body.String()
			}
			inFence, marker = false, ""
		case inFence:
			if marker != "" {
				body.WriteString(line + "\n")
			}
		case strings.HasPrefix(trimmed, "```"):
			inFence = true
			info := strings.Fields(strings.TrimPrefix(trimmed, "```"))
			if len(info) < 2 {
				continue
			}
			if len(info) != 2 || info[0] != "sh" || !slices.Contains(howtoMarkers, info[1]) {
				return nil, fmt.Errorf("line %d: unknown marker %q", i+1, strings.Join(info, " "))
			}
			if !strings.HasPrefix(line, "```") {
				return nil, fmt.Errorf("line %d: the block marked %q is not a top-level fence", i+1, info[1])
			}
			marker = info[1]
			body.Reset()
		}
	}
	if inFence {
		return nil, errors.New("the page ends inside a fenced block")
	}
	for _, m := range howtoMarkers {
		if _, ok := blocks[m]; !ok {
			return nil, fmt.Errorf("no block is marked %q", m)
		}
	}
	return blocks, nil
}

// howtoCheckVariables fails unless every $ that sh expands in body names a
// variable the block marked marker may read, inside double quotes. Quoting is
// tracked as sh does, so a $ inside single quotes, such as a jq variable, is
// not a reference.
func howtoCheckVariables(marker, body string) error {
	inSingle, inDouble := false, false
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case inSingle:
			inSingle = c != '\''
		case c == '\\':
			i++
		case c == '\'' && !inDouble:
			inSingle = true
		case c == '"':
			inDouble = !inDouble
		case c == '$':
			m := howtoVarName.FindStringSubmatch(body[i+1:])
			switch {
			case m == nil:
				return fmt.Errorf("the block marked %q expands %q, which is not a variable", marker, body[i:min(i+2, len(body))])
			case !slices.Contains(howtoVariables, m[1]) || (m[1] == "ARTIFACT_REF" && marker != "push"):
				return fmt.Errorf("the block marked %q references %s, which it may not read", marker, m[1])
			case !inDouble:
				return fmt.Errorf("the block marked %q references %s outside double quotes", marker, m[1])
			}
		}
	}
	return nil
}

// howtoWords splits block into its words, joining continued lines.
func howtoWords(block string) []string {
	return strings.Fields(strings.ReplaceAll(block, "\\\n", " "))
}

// howtoToolDecision returns why a spec that runs tools must be skipped, or
// must fail, given require, the value of ARTIFACT_REQUIRE_DOC_TOOLS, and
// lookPath. Both are empty when the spec runs.
func howtoToolDecision(require string, lookPath func(string) (string, error), tools ...string) (skip, fail string) {
	if require != "" && require != "true" {
		return "", fmt.Sprintf("%s is %q; its only accepted value is \"true\"", howtoRequireTools, require)
	}
	var missing []string
	for _, tool := range tools {
		if _, err := lookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) == 0 {
		return "", ""
	}
	why := fmt.Sprintf("the package artifacts how-to page needs %s, not found on PATH", strings.Join(missing, ", "))
	if require == "true" {
		return "", why + ", and " + howtoRequireTools + " is true"
	}
	return why + "; set " + howtoRequireTools + "=true to fail instead", ""
}

// howtoNeed skips or fails the running spec as howtoToolDecision decides for
// tools on this PATH.
func howtoNeed(tools ...string) {
	GinkgoHelper()
	skip, fail := howtoToolDecision(os.Getenv(howtoRequireTools), exec.LookPath, tools...)
	if fail != "" {
		Fail(fail)
	}
	if skip != "" {
		// Without -v, ginkgo run prints a skipped spec as a bare S unless it
		// has an always-visible report entry, which makes it print the spec
		// with its skip message, so make test names the missing tools.
		AddReportEntry("missing tools")
		Skip(skip)
	}
}

// howtoRun runs block with sh -e -c in dir, as a user shell without nounset or
// pipefail would, from this process's environment with every documented
// variable replaced by vars and DOCKER_CONFIG an empty directory. It asserts
// that block exits 0 when succeed is true, and with a positive status
// otherwise.
func howtoRun(succeed bool, dir, block string, vars map[string]string) {
	GinkgoHelper()
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "DOCKER_CONFIG" || slices.Contains(howtoVariables, name)
	})
	env = append(env, "DOCKER_CONFIG="+GinkgoT().TempDir())
	for name, value := range vars {
		env = append(env, name+"="+value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), howtoTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-e", "-c", block)
	cmd.Dir, cmd.Env = dir, env
	// A pipeline's children can hold the output pipe open after sh is killed.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	GinkgoWriter.Printf("$ %s%s", block, out)
	if succeed {
		Expect(err).NotTo(HaveOccurred(), "%s\n%s", block, out)
		return
	}
	var exitErr *exec.ExitError
	Expect(errors.As(err, &exitErr)).To(BeTrue(), "%s exited with %v\n%s", block, err, out)
	Expect(exitErr.ExitCode()).To(BeNumerically(">", 0), "%s\n%s", block, out)
}

var _ = Describe("the package artifacts how-to page", func() {
	Describe("howtoParse", func() {
		const valid = "```sh payload\ntar -C \"$PACKAGE_DIR\" .\n```\n\n" +
			"```sh metadata\njq --arg u \"$UNINSTALL\" '{uninstall: $u}' \"${PACKAGE_DIR:?}/config.json\"\n```\n\n" +
			"```sh push\noras push \\\n  \"$ARTIFACT_REF\"\n```\n\n" +
			"```sh push-layout\noras push --oci-layout l:t\n```\n\n" +
			"```sh convert\nmkdir \"$PACKAGE_DIR\"\ncrane export \"$IMAGE_REF\" -\n```\n\n" +
			"```json\n{\"$HOME\": 1}\n```\n"

		It("returns each marked block, with $ in single quotes and unmarked fences ignored", func() {
			blocks, err := howtoParse([]byte(valid))
			Expect(err).NotTo(HaveOccurred())
			Expect(blocks).To(HaveLen(len(howtoMarkers)))
			Expect(blocks).To(HaveKeyWithValue("push", "oras push \\\n  \"$ARTIFACT_REF\"\n"))
		})

		DescribeTable("rejecting a page",
			func(edit func(string) string, want string) {
				_, err := howtoParse([]byte(edit(valid)))
				Expect(err).To(MatchError(ContainSubstring(want)))
			},
			Entry("with an unknown marker", func(d string) string { return d + "```sh pushlayout\ntrue\n```\n" }, `unknown marker "sh pushlayout"`),
			Entry("with a marker on another language", func(d string) string { return d + "```bash payload\ntrue\n```\n" }, `unknown marker "bash payload"`),
			Entry("with a word after the marker", func(d string) string { return d + "```sh payload run\ntrue\n```\n" }, `unknown marker "sh payload run"`),
			Entry("with a repeated marker", func(d string) string { return d + "```sh payload\ntrue\n```\n" }, `a second block is marked "payload"`),
			Entry("with a missing marker", func(d string) string { return strings.Replace(d, "```sh convert", "```sh", 1) }, `no block is marked "convert"`),
			Entry("with a marked fence in a list", func(d string) string { return strings.Replace(d, "```sh convert", "  ```sh convert", 1) }, "not a top-level fence"),
			Entry("ending inside a fence", func(d string) string { return d + "```sh\n" }, "ends inside a fenced block"),
			Entry("with a variable outside the documented set", func(d string) string { return strings.Replace(d, `"$IMAGE_REF"`, `"$NODEWRIGHT_PACKAGE_REF"`, 1) }, "references NODEWRIGHT_PACKAGE_REF"),
			Entry("with ARTIFACT_REF outside push", func(d string) string { return strings.Replace(d, `"$UNINSTALL"`, `"$ARTIFACT_REF"`, 1) }, `"metadata" references ARTIFACT_REF`),
			Entry("with an unquoted variable", func(d string) string { return strings.Replace(d, `"$PACKAGE_DIR" .`, `$PACKAGE_DIR .`, 1) }, "PACKAGE_DIR outside double quotes"),
			Entry("with a command substitution", func(d string) string { return strings.Replace(d, `tar -C`, `tar -f "$(pwd)/p" -C`, 1) }, `expands "$("`),
		)
	})

	DescribeTable("howtoToolDecision",
		func(require string, missing []string, want string, mentions ...string) {
			lookPath := func(tool string) (string, error) {
				if slices.Contains(missing, tool) {
					return "", exec.ErrNotFound
				}
				return "/usr/bin/" + tool, nil
			}
			skip, fail := howtoToolDecision(require, lookPath, "tar", "jq", "oras", "crane")
			got := map[string]string{"run": "", "skip": skip, "fail": fail}
			for outcome, message := range got {
				if outcome == want {
					continue
				}
				Expect(message).To(BeEmpty(), outcome)
			}
			for _, m := range mentions {
				Expect(got[want]).To(ContainSubstring(m))
			}
		},
		Entry("runs with every tool and the variable unset", "", nil, "run"),
		Entry("runs with every tool and the variable true", "true", nil, "run"),
		Entry("skips naming each missing tool", "", []string{"oras", "crane"}, "skip", "oras, crane", howtoRequireTools+"=true"),
		Entry("fails naming the missing tool when the variable is true", "true", []string{"crane"}, "fail", "crane", howtoRequireTools+" is true"),
		Entry("fails on 1, naming the variable and its accepted value", "1", nil, "fail", howtoRequireTools+` is "1"`, `"true"`),
		Entry("fails on false even with a tool missing", "false", []string{"oras"}, "fail", howtoRequireTools+` is "false"`, `"true"`),
	)

	Describe("its commands", func() {
		var (
			ctx    context.Context
			blocks map[string]string
			// work is the scenario's working directory, outside every tree.
			work string
			// fixture is a package tree.
			fixture string
		)

		BeforeEach(func() {
			ctx = context.Background()
			doc, err := os.ReadFile(howtoDocPath)
			Expect(err).NotTo(HaveOccurred())
			blocks, err = howtoParse(doc)
			Expect(err).NotTo(HaveOccurred())
			work = GinkgoT().TempDir()
			fixture = layoutSource(layoutPayload...)
		})

		// layoutWords returns the words of push-layout and the index of its
		// --oci-layout, which an argument follows.
		layoutWords := func() ([]string, int) {
			GinkgoHelper()
			words := howtoWords(blocks["push-layout"])
			i := slices.Index(words, "--oci-layout")
			Expect(i).To(BeNumerically(">=", 0), "push-layout has no --oci-layout")
			Expect(len(words)).To(BeNumerically(">", i+1), "--oci-layout has no argument")
			return words, i
		}

		// layoutArg returns the directory and tag of the --oci-layout argument
		// of push-layout.
		layoutArg := func() (string, string) {
			GinkgoHelper()
			words, i := layoutWords()
			sep := strings.LastIndex(words[i+1], ":")
			Expect(sep).To(BeNumerically(">", 0), "--oci-layout %s has no tag", words[i+1])
			return words[i+1][:sep], words[i+1][sep+1:]
		}

		// inspect inspects what push-layout pushed into the layout in work.
		inspect := func() (*Inspection, error) {
			GinkgoHelper()
			dir, tag := layoutArg()
			ref, err := NewReference(howtoName, tag, "")
			Expect(err).NotTo(HaveOccurred())
			target, err := NewLayout(ctx, filepath.Join(work, dir), ref)
			Expect(err).NotTo(HaveOccurred())
			return Inspect(ctx, target)
		}

		// layOut lays in out at pkg beneath a new root, and returns that path.
		layOut := func(in *Inspection) (string, error) {
			GinkgoHelper()
			dir := GinkgoT().TempDir()
			root, err := os.OpenRoot(dir)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(root.Close)
			return filepath.Join(dir, "pkg"), Layout(ctx, in, root, "pkg")
		}

		// build runs payload and metadata on packageDir with uninstall, then
		// push-layout.
		build := func(packageDir, uninstall string) {
			GinkgoHelper()
			howtoRun(true, work, blocks["payload"], map[string]string{"PACKAGE_DIR": packageDir})
			howtoRun(true, work, blocks["metadata"], map[string]string{"PACKAGE_DIR": packageDir, "UNINSTALL": uninstall})
			howtoRun(true, work, blocks["push-layout"], nil)
		}

		// expectLaidOut lays out the package in the layout and expects the
		// tree of packageDir, uninstall as declared, and nothing else.
		expectLaidOut := func(packageDir string, uninstall artifact.Uninstall) {
			GinkgoHelper()
			in, err := inspect()
			Expect(err).NotTo(HaveOccurred())
			Expect(in.Kind).To(Equal(artifact.KindPackage))
			Expect(in.Package.Uninstall).To(Equal(uninstall))
			Expect(in.Package.HostFiles).To(BeEmpty())
			Expect(in.Package.Interrupt).To(BeNil())

			dir, _ := layoutArg()
			store, err := oci.NewFromFS(ctx, os.DirFS(filepath.Join(work, dir)))
			Expect(err).NotTo(HaveOccurred())
			data, err := content.FetchAll(ctx, store, in.Descriptor)
			Expect(err).NotTo(HaveOccurred())
			var m ocispec.Manifest
			Expect(json.Unmarshal(data, &m)).To(Succeed())
			Expect(m.ArtifactType).To(Equal(artifact.ArtifactTypePackage))

			dest, err := layOut(in)
			Expect(err).NotTo(HaveOccurred())
			want := extractTree(packageDir)
			Expect(want).To(HaveKeyWithValue("hostname", "-> /etc/hostname"))
			Expect(extractTree(dest)).To(Equal(want))
		}

		DescribeTable("builds a package of the tree that declares UNINSTALL",
			func(uninstall artifact.Uninstall) {
				howtoNeed("tar", "gzip", "jq", "oras")
				build(fixture, string(uninstall))
				expectLaidOut(fixture, uninstall)
			},
			Entry("supported", artifact.UninstallSupported),
			Entry("unsupported", artifact.UninstallUnsupported),
		)

		It("pushes to ARTIFACT_REF exactly what push-layout pushes into the layout", func() {
			push := blocks["push"]
			for _, mediaType := range []string{artifact.ArtifactTypePackage, artifact.MediaTypeMetadata, artifact.MediaTypePayload} {
				Expect(push).To(ContainSubstring(mediaType))
			}
			words, i := layoutWords()
			Expect(howtoWords(push)).To(Equal(slices.Concat(words[:i], []string{`"$ARTIFACT_REF"`}, words[i+2:])))
		})

		It("converts an image package into a package of its /skyhook-package alone, once per PACKAGE_DIR", func() {
			howtoNeed("crane", "tar", "gzip", "jq", "oras")
			reg := registrytest.New(registrytest.Options{})
			DeferCleanup(reg.Close)
			image := GinkgoT().TempDir()
			layoutWrite(filepath.Join(image, "skyhook-package"), layoutPayload...)
			layoutWrite(filepath.Join(image, "etc"), extractFile("os-release", 0o644, "ID=test\n"))
			layoutWrite(filepath.Join(image, "skyhook-package-old"), extractFile("config.json", 0o644, `{"old":true}`))
			_, err := registrytest.PushImage(ctx, reg.Repository("legacy/hello"), "1.0", image)
			Expect(err).NotTo(HaveOccurred())

			// The test registry's certificate is self-signed.
			convert := blocks["convert"]
			Expect(strings.Count(convert, "crane export")).To(Equal(1))
			convert = strings.Replace(convert, "crane export", "crane export --insecure", 1)
			vars := map[string]string{"PACKAGE_DIR": filepath.Join(GinkgoT().TempDir(), "pkg"), "IMAGE_REF": reg.Host() + "/legacy/hello:1.0"}

			howtoRun(true, work, convert, vars)
			build(vars["PACKAGE_DIR"], string(artifact.UninstallSupported))
			expectLaidOut(filepath.Join(image, "skyhook-package"), artifact.UninstallSupported)

			howtoRun(false, work, convert, vars)
		})

		Describe("degraded", func() {
			// with returns vars with name added, empty, when set is true.
			with := func(vars map[string]string, name string, set bool) map[string]string {
				if set {
					vars[name] = ""
				}
				return vars
			}

			DescribeTable("UNINSTALL builds a package that breaks the metadata schema",
				func(set bool) {
					howtoNeed("tar", "gzip", "jq", "oras")
					vars := with(map[string]string{"PACKAGE_DIR": fixture}, "UNINSTALL", set)
					howtoRun(true, work, blocks["payload"], vars)
					howtoRun(true, work, blocks["metadata"], vars)
					howtoRun(true, work, blocks["push-layout"], nil)
					_, err := inspect()
					Expect(err).To(MatchError(artifact.ErrInvalidArtifact))
					extractExpectReason(err, artifact.ReasonMetadataSchema)
				},
				Entry("unset", false),
				Entry("empty", true),
			)

			DescribeTable("PACKAGE_DIR fails payload and metadata",
				func(set bool) {
					howtoNeed("tar", "gzip", "jq")
					vars := with(map[string]string{"UNINSTALL": string(artifact.UninstallSupported)}, "PACKAGE_DIR", set)
					howtoRun(false, work, blocks["payload"], vars)
					howtoRun(false, work, blocks["metadata"], vars)
				},
				Entry("unset", false),
				Entry("empty", true),
			)

			DescribeTable("IMAGE_REF fails convert",
				func(set bool) {
					howtoNeed("crane", "tar")
					vars := with(map[string]string{"PACKAGE_DIR": filepath.Join(GinkgoT().TempDir(), "pkg")}, "IMAGE_REF", set)
					howtoRun(false, work, blocks["convert"], vars)
				},
				Entry("unset", false),
				Entry("empty", true),
			)

			It("fails metadata on a tree without config.json, and pushing the empty metadata.json it leaves breaks a descriptor", func() {
				howtoNeed("tar", "gzip", "jq", "oras")
				vars := map[string]string{"PACKAGE_DIR": layoutSource(layoutPayload[1:]...), "UNINSTALL": string(artifact.UninstallSupported)}
				howtoRun(true, work, blocks["payload"], vars)
				howtoRun(false, work, blocks["metadata"], vars)
				info, err := os.Stat(filepath.Join(work, "metadata.json"))
				Expect(err).NotTo(HaveOccurred())
				Expect(info.Size()).To(BeZero())

				howtoRun(true, work, blocks["push-layout"], nil)
				_, err = inspect()
				Expect(err).To(MatchError(artifact.ErrInvalidArtifact))
				extractExpectReason(err, artifact.ReasonDescriptor)
			})

			DescribeTable("a payload that does not carry the metadata's config.json is accepted and fails to lay out",
				func(payload []extractEntry) {
					howtoNeed("tar", "gzip", "jq", "oras")
					howtoRun(true, work, blocks["payload"], map[string]string{"PACKAGE_DIR": layoutSource(payload...)})
					howtoRun(true, work, blocks["metadata"], map[string]string{"PACKAGE_DIR": fixture, "UNINSTALL": string(artifact.UninstallSupported)})
					howtoRun(true, work, blocks["push-layout"], nil)
					in, err := inspect()
					Expect(err).NotTo(HaveOccurred())

					dest, err := layOut(in)
					extractExpectReason(err, artifact.ReasonConfigMismatch)
					_, err = os.Lstat(dest)
					Expect(err).To(MatchError(fs.ErrNotExist))
				},
				Entry("without config.json", layoutPayload[1:]),
				Entry("with a config.json that differs in a value", slices.Concat(
					[]extractEntry{extractFile("config.json", 0o644, strings.Replace(inspectConfig, `"1.0.0"`, `"1.0.1"`, 1))},
					layoutPayload[1:],
				)),
			)
		})
	})
})
