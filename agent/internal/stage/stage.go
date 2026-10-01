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

// Package stage defines the agent lifecycle stages (the keys of a package
// config's "modes" object) and the relationships between them. It is the
// vocabulary half of the package-config domain; the step package owns the
// other half (the steps that run within a stage).
package stage

import (
	"fmt"
	"slices"
)

// Stage identifies an agent lifecycle stage.
type Stage string

const (
	Uninstall          Stage = "uninstall"
	UninstallCheck     Stage = "uninstall-check"
	Upgrade            Stage = "upgrade"
	UpgradeCheck       Stage = "upgrade-check"
	Apply              Stage = "apply"
	ApplyCheck         Stage = "apply-check"
	Config             Stage = "config"
	ConfigCheck        Stage = "config-check"
	Interrupt          Stage = "interrupt"
	PostInterrupt      Stage = "post-interrupt"
	PostInterruptCheck Stage = "post-interrupt-check"
)

// All is every recognised stage. ParseStage validates wire mode keys against it
// (the JSON schema does not forbid extra keys, so this is the gate). Keep it in
// sync with the consts above when adding a stage.
var All = []Stage{
	Uninstall,
	UninstallCheck,
	Upgrade,
	UpgradeCheck,
	Apply,
	ApplyCheck,
	Config,
	ConfigCheck,
	Interrupt,
	PostInterrupt,
	PostInterruptCheck,
}

// ApplyToCheck maps each non-check stage to its check counterpart.
var ApplyToCheck = map[Stage]Stage{
	Uninstall:     UninstallCheck,
	Upgrade:       UpgradeCheck,
	Apply:         ApplyCheck,
	Config:        ConfigCheck,
	PostInterrupt: PostInterruptCheck,
}

// CheckToApply maps each check stage to its non-check counterpart.
var CheckToApply = map[Stage]Stage{
	UninstallCheck:     Uninstall,
	UpgradeCheck:       Upgrade,
	ApplyCheck:         Apply,
	ConfigCheck:        Config,
	PostInterruptCheck: PostInterrupt,
}

// nonStepStages is the Go counterpart to the legacy NON_STEP_MODES list.
// A "non-step" stage is one the agent dispatches without an apply/check
// companion: Interrupt today, possibly more in the future. Adding a
// stage here is enough to exclude it from IsStepStage.
var nonStepStages = map[Stage]struct{}{
	Interrupt: {},
}

// IsStepStage reports whether s carries normal step/check pairs.
// Non-step stages (see nonStepStages) are dispatched on their own.
func IsStepStage(s Stage) bool {
	_, isNonStep := nonStepStages[s]
	return !isNonStep
}

// ParseStage converts a wire mode key into a Stage, rejecting unknown values.
func ParseStage(s string) (Stage, error) {
	stage := Stage(s)
	if slices.Contains(All, stage) {
		return stage, nil
	}
	return "", fmt.Errorf("unknown stage %q", s)
}
