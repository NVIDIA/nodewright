//go:build !linux && !darwin

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
	"os"
	"runtime"
)

// exchange fails: no atomic swap of two directory entries is wired up for
// this GOOS, and Layout never falls back to a non-atomic replace.
func exchange(_ *os.File, _, to string) error {
	return fmt.Errorf("replacing %s atomically on %s: %w", to, runtime.GOOS, errors.ErrUnsupported)
}
