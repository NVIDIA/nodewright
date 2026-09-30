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

package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("command runner process execution", func() {
	It("returns a nonzero exit status without an operational error", func() {
		result, err := NewRunner().Run(context.Background(), helperCommand("exit", "7"))

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{ExitCode: 7}))
	})

	It("reports signal termination through SignalExitCode and Signal", func() {
		result, err := NewRunner().Run(context.Background(), helperCommand("signal"))

		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExitCode).To(Equal(SignalExitCode))
		Expect(result.Signal).To(Equal(os.Signal(syscall.SIGTERM)))
	})

	It("lets a running process finish after cancellation and refuses to start another", func() {
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

		// The helper reports readiness and then keeps running, so cancelling on
		// readiness lands mid-run. The process must complete on its own and
		// report its real exit status; it must not be killed.
		start := time.Now()
		result, err := NewRunner().Run(ctx, helperCommand(
			"sleep", "300",
			WithStdout(&readinessWriter{ready: ready}),
		))

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{ExitCode: 0}))
		Expect(time.Since(start)).To(BeNumerically(">=", 300*time.Millisecond))

		// Only a command that has not yet started is refused.
		_, err = NewRunner().Run(ctx, helperCommand("exit", "0"))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	It("propagates output writer failures", func() {
		writeErr := errors.New("stdout unavailable")
		cmd := helperCommand("stdout", WithStdout(errorWriter{err: writeErr}))

		_, err := NewRunner().Run(context.Background(), cmd)

		Expect(errors.Is(err, writeErr)).To(BeTrue())
	})
})

type errorWriter struct {
	err error
}

func (writer errorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

type readinessWriter struct {
	ready chan struct{}
	once  sync.Once
}

func (writer *readinessWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.ready) })
	return len(data), nil
}

func helperCommand(action string, valuesAndOptions ...any) Command {
	values := make([]string, 0, len(valuesAndOptions))
	options := []CommandOption{}
	for _, value := range valuesAndOptions {
		switch typed := value.(type) {
		case string:
			values = append(values, typed)
		case CommandOption:
			options = append(options, typed)
		default:
			panic(fmt.Sprintf("unsupported helper command value %T", value))
		}
	}
	arguments := append([]string{"-test.run=^TestCommand$", "--", action}, values...)
	options = append([]CommandOption{WithArguments(arguments...)}, options...)
	return NewCommand(commandTestExecutable(), options...)
}

func commandTestExecutable() string {
	executable, err := filepath.Abs(os.Args[0])
	Expect(err).NotTo(HaveOccurred())
	return executable
}

func runCommandTestHelper() bool {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return false
	}
	if separator+1 >= len(os.Args) {
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
		_, _ = fmt.Fprintf(os.Stdout, "environment=%s\n", os.Getenv("NODEWRIGHT_VALUE"))
		_, _ = fmt.Fprintf(os.Stdout, "working-directory=%s\n", workingDirectory)
		_, _ = fmt.Fprintln(os.Stderr, "helper-stderr")
		os.Exit(0)
	case "stdout":
		_, _ = io.WriteString(os.Stdout, "output")
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
		_, _ = io.WriteString(os.Stdout, "ready")
		time.Sleep(time.Hour)
	case "sleep":
		milliseconds, err := strconv.Atoi(values[0])
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.WriteString(os.Stdout, "ready")
		time.Sleep(time.Duration(milliseconds) * time.Millisecond)
		os.Exit(0)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown helper action %q\n", action)
		os.Exit(2)
	}
	return true
}
