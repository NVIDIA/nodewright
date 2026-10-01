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

package interrupts

import (
	"context"
	"errors"

	"github.com/NVIDIA/nodewright/agent/internal/execution"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RestartAllServices", func() {
	It("uses the restart_all_services wire type", func() {
		Expect(RestartAllServices{}.Type()).To(Equal(RestartAllServicesType))
	})

	It("validates context and execution configuration", func() {
		var nilContext context.Context
		status, err := RestartAllServices{}.Run(nilContext, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError("running interrupt: context is nil"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, err = RestartAllServices{}.Run(ctx, newInterruptRunConfig())
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())

		status, err = RestartAllServices{}.Run(context.Background(), execution.Config{})
		Expect(status).To(Equal(execution.StatusFailed))
		Expect(err).To(MatchError(ContainSubstring("invalid run config")))
	})
})
