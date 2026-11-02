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

// Package memlimit derives the Go runtime's soft memory limit from the container's cgroup
// memory limit, so the garbage collector works harder as the heap nears the limit instead of
// letting it grow to twice the live heap and leaving the kernel OOM killer as the only brake.
package memlimit

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	// DefaultCgroupRoot is where the container runtime mounts the container's own cgroup.
	DefaultCgroupRoot = "/sys/fs/cgroup"

	// GoMemLimitEnv is read by the Go runtime itself at startup.
	GoMemLimitEnv = "GOMEMLIMIT"

	cgroupV2File = "memory.max"
	cgroupV1File = "memory/memory.limit_in_bytes"

	// cgroup v1 has no "max" keyword: an unlimited cgroup reports the page counter maximum
	// times the page size, about 2^63. No real container limit comes anywhere near 2^62.
	cgroupV1Unlimited = int64(1) << 62
)

// Source says where the soft memory limit came from.
type Source string

const (
	SourceGoMemLimit Source = "GOMEMLIMIT"
	SourceCgroupV2   Source = "cgroup v2"
	SourceCgroupV1   Source = "cgroup v1"
	SourceNone       Source = "none"
)

// Result describes what Configure did.
type Result struct {
	Source Source
	// ContainerLimit is the cgroup memory limit in bytes, zero unless Source is a cgroup.
	ContainerLimit int64
	// SoftLimit is the runtime's soft memory limit in bytes after Configure returns.
	SoftLimit int64
}

// Configure sets the Go runtime's soft memory limit to fraction of the memory limit of the
// cgroup mounted at cgroupRoot. It leaves the runtime alone when GOMEMLIMIT is set, so an
// explicit value always wins, and when the container has no memory limit.
func Configure(cgroupRoot string, fraction float64) (Result, error) {
	if math.IsNaN(fraction) || fraction <= 0 || fraction > 1 {
		return Result{}, fmt.Errorf("memory limit fraction must be greater than 0 and at most 1, got %v", fraction)
	}

	if os.Getenv(GoMemLimitEnv) != "" {
		return Result{Source: SourceGoMemLimit, SoftLimit: debug.SetMemoryLimit(-1)}, nil
	}

	limit, source, err := containerLimit(cgroupRoot)
	if err != nil {
		return Result{}, err
	}
	if source == SourceNone {
		return Result{Source: SourceNone, SoftLimit: debug.SetMemoryLimit(-1)}, nil
	}

	soft := int64(float64(limit) * fraction)
	if soft <= 0 {
		return Result{}, fmt.Errorf("memory limit fraction %v of a %d byte limit leaves no soft limit", fraction, limit)
	}
	debug.SetMemoryLimit(soft)
	return Result{Source: source, ContainerLimit: limit, SoftLimit: soft}, nil
}

func containerLimit(cgroupRoot string) (int64, Source, error) {
	raw, err := os.ReadFile(filepath.Join(cgroupRoot, cgroupV2File))
	if err == nil {
		value := strings.TrimSpace(string(raw))
		if value == "max" {
			return 0, SourceNone, nil
		}
		limit, err := parseLimit(cgroupV2File, value)
		return limit, SourceCgroupV2, err
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, SourceNone, fmt.Errorf("reading %s: %w", cgroupV2File, err)
	}

	raw, err = os.ReadFile(filepath.Join(cgroupRoot, cgroupV1File))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, SourceNone, nil
	}
	if err != nil {
		return 0, SourceNone, fmt.Errorf("reading %s: %w", cgroupV1File, err)
	}
	limit, err := parseLimit(cgroupV1File, strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, SourceNone, err
	}
	if limit >= cgroupV1Unlimited {
		return 0, SourceNone, nil
	}
	return limit, SourceCgroupV1, nil
}

func parseLimit(file, value string) (int64, error) {
	limit, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", file, err)
	}
	if limit <= 0 {
		return 0, fmt.Errorf("parsing %s: limit must be positive, got %d", file, limit)
	}
	return limit, nil
}
