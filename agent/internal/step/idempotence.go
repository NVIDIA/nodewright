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
	"encoding/json"
	"errors"
	"fmt"
)

// Idempotence controls how the agent treats step re-runs.
type Idempotence string

const (
	// Auto means the agent runs the step once or until success.
	Auto Idempotence = "auto"
	// Disabled means the step itself decides; the agent may invoke it
	// many times.
	Disabled Idempotence = "disabled"
)

// Validate reports whether the receiver is a recognised value.
func (i Idempotence) Validate() error {
	switch i {
	case Auto, Disabled:
		return nil
	default:
		return fmt.Errorf("%s is not a valid idempotence value", i)
	}
}

// MarshalJSON encodes idempotence as the wire bool the legacy agent uses:
// true == Disabled (the step manages its own idempotence), false ==
// Auto. Owning the codec here lets the step structs serialize via
// stdlib without a custom marshaler.
func (i Idempotence) MarshalJSON() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, fmt.Errorf("marshal idempotence: %w", err)
	}
	return json.Marshal(i == Disabled)
}

// UnmarshalJSON decodes the wire bool back into the enum: true ->
// Disabled and false -> Auto. Missing fields are defaulted separately.
func (i *Idempotence) UnmarshalJSON(data []byte) error {
	var value *bool
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("unmarshal idempotence: %w", err)
	}
	if value == nil {
		return errors.New("unmarshal idempotence: null is not a valid idempotence value")
	}
	if *value {
		*i = Disabled
	} else {
		*i = Auto
	}
	return nil
}
