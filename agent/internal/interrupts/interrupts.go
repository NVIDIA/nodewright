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

// Package interrupts decodes, encodes, and runs agent interrupt operations.
//
// Each Interrupt identifies its wire type with Type, executes its own command
// composition through Run using execution.Config and execution.Status, and
// preserves its operator-facing wire representation through Serialize.
package interrupts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/NVIDIA/nodewright/agent/internal/execution"
)

// errInvalidSerializedInterrupt is returned when a serialized interrupt is not
// a base64-encoded JSON object of the expected shape. It is a sentinel so
// callers can branch on it with errors.Is.
var errInvalidSerializedInterrupt = errors.New(`serialized interrupt must be base64-encoded JSON with a "type" field`)

// InterruptType identifies a supported interrupt implementation on the wire.
type InterruptType string

const (
	NodeRestartType        InterruptType = "node_restart"
	ServiceRestartType     InterruptType = "service_restart"
	RestartAllServicesType InterruptType = "restart_all_services"
	NoOpType               InterruptType = "no_op"
	ScriptInterruptType    InterruptType = "script_interrupt"
)

// Interrupt is the contract satisfied by every interrupt type.
type Interrupt interface {
	Type() InterruptType
	Run(context.Context, execution.Config) (execution.Status, error)
	Serialize() ([]byte, error)
}

// ResumableInterrupt exposes command-level progress so the orchestrator can
// preserve the legacy agent's indexed completion markers across retries.
type ResumableInterrupt interface {
	Interrupt
	OperationCount() int
	RunPending(
		context.Context,
		execution.Config,
		[]bool,
		func(int) error,
	) (execution.Status, error)
}

func validateRun(ctx context.Context, config execution.Config, interruptType InterruptType) error {
	if ctx == nil {
		return errors.New("running interrupt: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("running interrupt %q: %w", interruptType, err)
	}
	if err := config.Validate(); err != nil {
		return fmt.Errorf("running interrupt %q: invalid run config: %w", interruptType, err)
	}
	return nil
}

// Encode serializes an Interrupt to the base64+JSON wire form expected
// by the operator's interrupt controller input. A nil receiver is
// rejected explicitly so a misuse from a caller returns a typed error
// rather than panicking on a nil interface method call.
func Encode(i Interrupt) (string, error) {
	if i == nil {
		return "", errors.New("nil interrupt")
	}
	data, err := i.Serialize()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// Decode parses a base64+JSON serialized interrupt produced by Encode
// (or its legacy counterpart skyhook_agent.interrupts.inflate).
// Wire-shape errors return errInvalidSerializedInterrupt; unknown type
// names return a descriptive error listing the supported types.
func Decode(serializedValue string) (Interrupt, error) {
	data, err := base64.StdEncoding.DecodeString(serializedValue)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidSerializedInterrupt, err)
	}

	// type is probed as *string to distinguish an absent field (a wire-shape
	// error) from a present-but-empty value (an unknown-type error); the two
	// take different branches below.
	var head struct {
		Type *InterruptType `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidSerializedInterrupt, err)
	}
	if head.Type == nil {
		return nil, errInvalidSerializedInterrupt
	}

	switch *head.Type {
	case NodeRestartType:
		return NodeRestart{}, nil
	case ServiceRestartType:
		var wire struct {
			Services []string `json:"services"`
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			return nil, fmt.Errorf("%w: %v", errInvalidSerializedInterrupt, err)
		}
		return ServiceRestart{Services: wire.Services}, nil
	case RestartAllServicesType:
		return RestartAllServices{}, nil
	case NoOpType:
		return NoOp{}, nil
	case ScriptInterruptType:
		return ScriptInterrupt{}, nil
	default:
		return nil, fmt.Errorf(
			"unknown interrupt %q must be one of: %s",
			*head.Type,
			supportedTypes(),
		)
	}
}

// supportedTypes returns the known interrupt types in Decode dispatch order.
func supportedTypes() string {
	return strings.Join([]string{
		string(NodeRestartType),
		string(ServiceRestartType),
		string(RestartAllServicesType),
		string(NoOpType),
		string(ScriptInterruptType),
	}, ", ")
}

// marshalTypeOnly serializes an interrupt whose wire form is just
// {"type": "..."}.
func marshalTypeOnly(typeName InterruptType) ([]byte, error) {
	return json.Marshal(struct {
		Type InterruptType `json:"type"`
	}{Type: typeName})
}
