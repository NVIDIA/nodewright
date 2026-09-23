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

package step

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NVIDIA/nodewright/agent/internal/command"
	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RegularStep.applyDefaults", func() {
	It("fills name from path when name is empty", func() {
		s := RegularStep{ScriptPath: "foo.sh"}
		s.applyDefaults()
		Expect(s.Name).To(Equal("foo.sh"))
	})

	It("preserves name when it is set", func() {
		s := RegularStep{Name: "explicit", ScriptPath: "foo.sh"}
		s.applyDefaults()
		Expect(s.Name).To(Equal("explicit"))
	})

	It("defaults arguments to an empty slice", func() {
		s := RegularStep{ScriptPath: "foo.sh"}
		s.applyDefaults()
		Expect(s.Arguments).To(Equal([]string{}))
	})

	It("defaults returncodes to [0]", func() {
		s := RegularStep{ScriptPath: "foo.sh"}
		s.applyDefaults()
		Expect(s.Returncodes).To(Equal([]command.ExitCode{command.SuccessExitCode}))
	})

	It("defaults env to an empty map", func() {
		s := RegularStep{ScriptPath: "foo.sh"}
		s.applyDefaults()
		Expect(s.Env).To(Equal(map[string]string{}))
	})

	It("defaults idempotence to Auto", func() {
		s := RegularStep{ScriptPath: "foo.sh"}
		s.applyDefaults()
		Expect(s.Idempotence()).To(Equal(Auto))
	})

	It("does not change OnHost after the constructor or Decode seeds it", func() {
		s := RegularStep{ScriptPath: "foo.sh", OnHost: true}
		s.applyDefaults()
		Expect(s.OnHost).To(BeTrue())
	})
})

var _ = Describe("RegularStep.Encode", func() {
	It("emits upgrade_step:false", func() {
		s := RegularStep{ScriptPath: "foo.sh"}
		dumped, err := s.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(dumped)).To(ContainSubstring(`"upgrade_step":false`))
	})

	It("does not mutate the caller's value when applying defaults", func() {
		s := RegularStep{ScriptPath: "foo.sh"}
		_, err := s.Encode()
		Expect(err).NotTo(HaveOccurred())

		Expect(s.Name).To(Equal(""), "Encode received a value copy; the caller's RegularStep must be untouched")
		Expect(s.Idempotence()).To(Equal(Idempotence("")))
	})

	It("rejects an explicitly invalid idempotence value", func() {
		s := RegularStep{
			ScriptPath:      "foo.sh",
			IdempotenceMode: "bogus",
		}
		_, err := s.Encode()
		Expect(err).To(MatchError(ContainSubstring("bogus is not a valid idempotence value")))
	})
})

var _ = Describe("NewRegularStep", func() {
	It("applies all defaults when only path is provided", func() {
		s := NewRegularStep("foo.sh")
		Expect(s.ScriptPath).To(Equal("foo.sh"))
		Expect(s.Name).To(Equal("foo.sh"))
		Expect(s.Arguments).To(Equal([]string{}))
		Expect(s.Returncodes).To(Equal([]command.ExitCode{command.SuccessExitCode}))
		Expect(s.Env).To(Equal(map[string]string{}))
		Expect(s.OnHost).To(BeTrue())
		Expect(s.Idempotence()).To(Equal(Auto))
		Expect(s.RequiresInterrupt).To(BeFalse())
	})

	It("honors WithName instead of the path fallback", func() {
		s := NewRegularStep("foo.sh", WithName("explicit"))
		Expect(s.Name).To(Equal("explicit"))
	})

	It("honors WithOnHost(false) over the on-by-default value", func() {
		s := NewRegularStep("foo.sh", WithOnHost(false))
		Expect(s.OnHost).To(BeFalse())
	})

	It("applies every option when combined", func() {
		s := NewRegularStep(
			"foo.sh",
			WithName("custom"),
			WithArguments([]string{"-x", "y"}),
			WithReturncodes([]command.ExitCode{0, 2}),
			WithEnv(map[string]string{"K": "V"}),
			WithOnHost(false),
			WithIdempotence(Disabled),
			WithRequiresInterrupt(true),
		)
		Expect(s.Name).To(Equal("custom"))
		Expect(s.Arguments).To(Equal([]string{"-x", "y"}))
		Expect(s.Returncodes).To(Equal([]command.ExitCode{0, 2}))
		Expect(s.Env).To(Equal(map[string]string{"K": "V"}))
		Expect(s.OnHost).To(BeFalse())
		Expect(s.Idempotence()).To(Equal(Disabled))
		Expect(s.RequiresInterrupt).To(BeTrue())
	})

	It("round-trips through Encode/Decode", func() {
		start := NewRegularStep("foo.sh", WithIdempotence(Disabled))
		dumped, err := start.Encode()
		Expect(err).NotTo(HaveOccurred())

		round, err := Decode(dumped)
		Expect(err).NotTo(HaveOccurred())
		Expect(round.(RegularStep).ScriptPath).To(Equal("foo.sh"))
		Expect(round.(RegularStep).Idempotence()).To(Equal(Disabled))
	})
})

var _ = Describe("RegularStep.Run", func() {
	It("executes a portable step with arguments, environment, working directory, and output", func() {
		stepRoot, skyhookDir, executable := prepareStepTestExecutable()
		GinkgoT().Setenv("NODEWRIGHT_STEP_VALUE", "from-parent")
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		config := newStepRunConfig(stepRoot, skyhookDir, execution.WithRunOutput(stdout, stderr))
		value := NewRegularStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithArguments([]string{
				"-test.run=^TestStep$", "--", "inspect", "literal", "env:NODEWRIGHT_STEP_VALUE",
			}),
			WithEnv(map[string]string{
				"NODEWRIGHT_CUSTOM": "configured",
				stepRootEnv:         "configured-step-root",
				skyhookDirEnv:       "configured-skyhook-dir",
			}),
		)

		status, err := value.Run(context.Background(), config)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(stdout.String()).To(Equal(fmt.Sprintf(
			"arguments=literal,from-parent\ncustom=configured\nstep-root=%s\nskyhook-dir=%s\nworking-directory=%s\n",
			stepRoot,
			skyhookDir,
			skyhookDir,
		)))
		Expect(stderr.String()).To(Equal("step-helper-stderr\n"))
	})

	It("resolves non-host paths through the mounted host root", func() {
		rootMount := GinkgoT().TempDir()
		stepRoot := "/package/steps"
		skyhookDir := "/package"
		mountedStepRoot := filepath.Join(rootMount, "package", "steps")
		mountedSkyhookDir := filepath.Join(rootMount, "package")
		Expect(os.MkdirAll(mountedStepRoot, 0o755)).To(Succeed())
		testExecutable, err := filepath.Abs(os.Args[0])
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(testExecutable)
		Expect(err).NotTo(HaveOccurred())
		executable := filepath.Join(mountedStepRoot, "step-helper")
		Expect(os.WriteFile(executable, data, 0o700)).To(Succeed())
		stdout := &bytes.Buffer{}
		config, err := execution.NewConfig(
			execution.WithRootMount(rootMount),
			execution.WithStepRoot(stepRoot),
			execution.WithSkyhookDir(skyhookDir),
			execution.WithRunOutput(stdout, io.Discard),
		)
		Expect(err).NotTo(HaveOccurred())
		value := NewRegularStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithArguments([]string{"-test.run=^TestStep$", "--", "inspect"}),
		)

		status, err := value.Run(context.Background(), config)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(stdout.String()).To(ContainSubstring("step-root=" + mountedStepRoot))
		Expect(stdout.String()).To(ContainSubstring("skyhook-dir=" + mountedSkyhookDir))
		Expect(stdout.String()).To(ContainSubstring("working-directory=" + mountedSkyhookDir))
	})

	It("uses the configured accepted return codes", func() {
		stepRoot, skyhookDir, executable := prepareStepTestExecutable()
		arguments := []string{"-test.run=^TestStep$", "--", "exit", "7"}
		config := newStepRunConfig(stepRoot, skyhookDir)

		accepted := NewRegularStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithArguments(arguments),
			WithReturncodes([]command.ExitCode{7}),
		)
		status, err := accepted.Run(context.Background(), config)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))

		rejected := NewRegularStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithArguments(arguments),
		)
		status, err = rejected.Run(context.Background(), config)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
	})

	It("fails when the step is terminated by a signal", func() {
		stepRoot, skyhookDir, executable := prepareStepTestExecutable()
		value := NewRegularStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithArguments([]string{"-test.run=^TestStep$", "--", "signal"}),
		)

		status, err := value.Run(context.Background(), newStepRunConfig(stepRoot, skyhookDir))

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusFailed))
	})

	It("reports all missing argument environment variables before execution", func() {
		missingNames := []string{"NODEWRIGHT_MISSING_ONE", "NODEWRIGHT_MISSING_TWO"}
		for _, name := range missingNames {
			value, exists := os.LookupEnv(name)
			Expect(os.Unsetenv(name)).To(Succeed())
			DeferCleanup(func() {
				if exists {
					Expect(os.Setenv(name, value)).To(Succeed())
					return
				}
				Expect(os.Unsetenv(name)).To(Succeed())
			})
		}

		stepRoot, skyhookDir, executable := prepareStepTestExecutable()
		value := NewRegularStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithArguments([]string{"env:" + missingNames[0], "env:" + missingNames[1]}),
		)

		status, err := value.Run(context.Background(), newStepRunConfig(stepRoot, skyhookDir))

		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring(
			"expected environment variables do not exist: " + strings.Join(missingNames, ", "),
		)))
	})

	It("lets an in-flight step finish after cancellation", func() {
		stepRoot, skyhookDir, executable := prepareStepTestExecutable()
		value := NewRegularStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithArguments([]string{"-test.run=^TestStep$", "--", "sleep", "300"}),
		)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ready := make(chan struct{})
		go func() {
			select {
			case <-ready:
				cancel()
			case <-ctx.Done():
			}
		}()

		// Cancelling on the helper's readiness write, rather than on a timer,
		// guarantees the cancellation lands while the step is running and not
		// before it starts, where runStep's ctx.Err() check would refuse it.
		// The step must complete on its own and report its real outcome; only
		// a step that has not started yet is refused (covered by runSteps tests).
		start := time.Now()
		status, err := value.Run(ctx, newStepRunConfig(
			stepRoot,
			skyhookDir,
			execution.WithRunOutput(&readinessWriter{ready: ready}, io.Discard),
		))

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(time.Since(start)).To(BeNumerically(">=", 300*time.Millisecond))
	})
})

var _ = Describe("RegularStep.WithVersions", func() {
	It("injects versions into the execution environment", func() {
		stepRoot, skyhookDir, executable := prepareStepTestExecutable()
		stdout := &bytes.Buffer{}
		configured := NewRegularStep(
			filepath.Base(executable),
			WithOnHost(false),
			WithArguments([]string{"-test.run=^TestStep$", "--", "versions"}),
		)

		status, err := configured.WithVersions("previous", "current").Run(
			context.Background(),
			newStepRunConfig(
				stepRoot,
				skyhookDir,
				execution.WithRunOutput(stdout, io.Discard),
			),
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(execution.StatusSuccess))
		Expect(stdout.String()).To(Equal("versions=previous,current\n"))
	})

	It("keeps runtime versions out of serialized configuration", func() {
		configured := NewRegularStep("upgrade.sh", WithEnv(map[string]string{"CUSTOM": "value"}))

		prepared := configured.WithVersions("1.0.0", "2.0.0").(RegularStep)

		Expect(configured.versions).To(BeNil())
		Expect(prepared.versions).NotTo(BeNil())
		Expect(prepared.Env).To(Equal(map[string]string{"CUSTOM": "value"}))
		dumped, err := prepared.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(dumped)).NotTo(ContainSubstring(previousVersionEnv))
		Expect(string(dumped)).NotTo(ContainSubstring(currentVersionEnv))
	})
})

type readinessWriter struct {
	ready chan struct{}
	once  sync.Once
}

func (writer *readinessWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.ready) })
	return len(data), nil
}

func newStepRunConfig(stepRoot, skyhookDir string, options ...execution.Option) execution.Config {
	options = append([]execution.Option{
		execution.WithRootMount("/"),
		execution.WithStepRoot(stepRoot),
		execution.WithSkyhookDir(skyhookDir),
		execution.WithRunOutput(io.Discard, io.Discard),
	}, options...)
	config, err := execution.NewConfig(options...)
	Expect(err).NotTo(HaveOccurred())
	return config
}

func prepareStepTestExecutable() (string, string, string) {
	stepRoot := GinkgoT().TempDir()
	skyhookDir := GinkgoT().TempDir()
	executable := filepath.Join(stepRoot, "step-helper")
	testExecutable, err := filepath.Abs(os.Args[0])
	Expect(err).NotTo(HaveOccurred())
	data, err := os.ReadFile(testExecutable)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(executable, data, 0o700)).To(Succeed())
	return stepRoot, skyhookDir, executable
}

func runStepTestHelper() bool {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return false
	}
	action := os.Args[separator+1]
	values := os.Args[separator+2:]
	switch action {
	case "inspect":
		workingDirectory, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(os.Stdout, "arguments=%s\n", strings.Join(values, ","))
		_, _ = fmt.Fprintf(os.Stdout, "custom=%s\n", os.Getenv("NODEWRIGHT_CUSTOM"))
		_, _ = fmt.Fprintf(os.Stdout, "step-root=%s\n", os.Getenv("STEP_ROOT"))
		_, _ = fmt.Fprintf(os.Stdout, "skyhook-dir=%s\n", os.Getenv("SKYHOOK_DIR"))
		_, _ = fmt.Fprintf(os.Stdout, "working-directory=%s\n", workingDirectory)
		_, _ = fmt.Fprintln(os.Stderr, "step-helper-stderr")
		os.Exit(0)
	case "exit":
		code, err := strconv.Atoi(values[0])
		if err != nil {
			os.Exit(2)
		}
		os.Exit(code)
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(time.Hour)
	case "wait":
		time.Sleep(time.Hour)
	case "sleep":
		milliseconds, err := strconv.Atoi(values[0])
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.WriteString(os.Stdout, "ready")
		time.Sleep(time.Duration(milliseconds) * time.Millisecond)
		os.Exit(0)
	case "versions":
		_, _ = fmt.Fprintf(
			os.Stdout,
			"versions=%s,%s\n",
			os.Getenv(previousVersionEnv),
			os.Getenv(currentVersionEnv),
		)
		os.Exit(0)
	default:
		os.Exit(2)
	}
	return true
}
