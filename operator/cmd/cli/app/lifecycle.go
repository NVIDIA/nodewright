/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package app

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NVIDIA/nodewright/operator/internal/cli/client"
	cliContext "github.com/NVIDIA/nodewright/operator/internal/cli/context"
	"github.com/NVIDIA/nodewright/operator/internal/cli/utils"
)

// lifecycleConfig defines the configuration for a lifecycle command
type lifecycleConfig struct {
	use          string
	short        string
	long         string
	example      string
	annotation   string
	action       string // "set" or "remove"
	verb         string // past tense for output message (e.g., "paused", "resumed")
	confirmVerb  string // verb for confirmation prompt (e.g., "pause", "disable")
	needsConfirm bool
}

// lifecycleOptions holds the options for lifecycle commands that need confirmation
type lifecycleOptions struct {
	confirm bool
}

// newLifecycleCmd creates a lifecycle command based on the provided configuration
func newLifecycleCmd(ctx *cliContext.CLIContext, cfg lifecycleConfig) *cobra.Command {
	opts := &lifecycleOptions{}

	cmd := &cobra.Command{
		Use:     cfg.use,
		Short:   cfg.short,
		Long:    cfg.long,
		Example: cfg.example,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			skyhookName := args[0]

			if cfg.needsConfirm && !opts.confirm {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "This will %s Skyhook %q. Continue? [y/N]: ",
					cfg.confirmVerb, skyhookName)
				reader := bufio.NewReader(cmd.InOrStdin())
				response, err := reader.ReadString('\n')
				if err != nil {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
					return nil
				}
				response = strings.TrimSpace(response)
				if response != "y" && response != "Y" {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
					return nil
				}
			}

			// Check dry-run before making changes
			if ctx.GlobalFlags.DryRun {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] Would %s Skyhook %q\n", cfg.confirmVerb, skyhookName)
				return nil
			}

			clientFactory := client.NewFactory(ctx.GlobalFlags.ConfigFlags)
			kubeClient, err := clientFactory.Client()
			if err != nil {
				return fmt.Errorf("initializing kubernetes client: %w", err)
			}

			// Fetch the Skyhook to check operator version from its annotation
			skyhook, err := utils.GetSkyhook(cmd.Context(), kubeClient.Dynamic(), skyhookName)
			if err != nil {
				return err
			}

			// Check operator version compatibility using Skyhook's version annotation
			// If annotation is missing or invalid (e.g., "dev"), fall back to deployment version
			opVersion := utils.GetSkyhookVersion(skyhook)
			if opVersion == "" || !utils.IsValidVersion(opVersion) {
				// Try to get version from deployment instead
				deployVersion, err := utils.DiscoverOperatorVersion(cmd.Context(), kubeClient.Kubernetes(), ctx.GlobalFlags.Namespace())
				if err == nil && utils.IsValidVersion(deployVersion) {
					opVersion = deployVersion
				} else {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: unable to determine operator version; "+
						"cannot verify compatibility (requires %s+)\n", utils.MinAnnotationSupportVersion)
				}
			}

			if utils.IsValidVersion(opVersion) && utils.CompareVersions(opVersion, utils.MinAnnotationSupportVersion) < 0 {
				// Operator is older than v0.8.0 - annotations won't work
				if cfg.annotation == utils.PauseAnnotation {
					// pause/resume - feature exists but uses spec field instead of annotation
					specValue := "true"
					if cfg.action == "remove" {
						specValue = "false"
					}
					return fmt.Errorf("operator version %s uses spec.pause instead of annotations; "+
						"use 'kubectl edit skyhook %s' and set spec.pause: %s", opVersion, skyhookName, specValue)
				}
				// disable/enable - feature doesn't exist at all in older versions
				return fmt.Errorf("operator version %s does not support %s; this feature was added in %s",
					opVersion, cfg.confirmVerb, utils.MinAnnotationSupportVersion)
			}

			if cfg.action == "set" {
				err = utils.SetSkyhookAnnotation(cmd.Context(), kubeClient.Dynamic(), skyhookName, cfg.annotation, "true")
			} else {
				err = utils.RemoveSkyhookAnnotation(cmd.Context(), kubeClient.Dynamic(), skyhookName, cfg.annotation)
			}
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skyhook %q %s\n", skyhookName, cfg.verb)
			return nil
		},
	}

	if cfg.needsConfirm {
		cmd.Flags().BoolVarP(&opts.confirm, "confirm", "y", false, "Skip confirmation prompt")
	}

	return cmd
}

// NewPauseCmd creates the pause command
func NewPauseCmd(ctx *cliContext.CLIContext) *cobra.Command {
	return newLifecycleCmd(ctx, lifecycleConfig{
		use:   "pause <skyhook-name>",
		short: "Pause a Skyhook from processing",
		long: `Pause a Skyhook by setting the pause annotation.

When a Skyhook is paused, the operator will stop processing new nodes
but will not interrupt any currently running operations.`,
		example: `  # Pause a Skyhook
  kubectl skyhook pause gpu-init

  # Pause without confirmation
  kubectl skyhook pause gpu-init --confirm`,
		annotation:   utils.PauseAnnotation,
		action:       "set",
		verb:         "paused",
		confirmVerb:  "pause",
		needsConfirm: true,
	})
}

// NewResumeCmd creates the resume command
func NewResumeCmd(ctx *cliContext.CLIContext) *cobra.Command {
	return newLifecycleCmd(ctx, lifecycleConfig{
		use:   "resume <skyhook-name>",
		short: "Resume a paused Skyhook",
		long: `Resume a paused Skyhook by removing the pause annotation.

The operator will resume processing nodes after this command.`,
		example: `  # Resume a paused Skyhook
  kubectl skyhook resume gpu-init

  # Resume without confirmation
  kubectl skyhook resume gpu-init --confirm`,
		annotation:   utils.PauseAnnotation,
		action:       "remove",
		verb:         "resumed",
		confirmVerb:  "resume",
		needsConfirm: true,
	})
}

// NewDisableCmd creates the disable command
func NewDisableCmd(ctx *cliContext.CLIContext) *cobra.Command {
	return newLifecycleCmd(ctx, lifecycleConfig{
		use:   "disable <skyhook-name>",
		short: "Disable a Skyhook completely",
		long: `Disable a Skyhook by setting the disable annotation.

When a Skyhook is disabled, the operator will completely stop processing
and the Skyhook will be effectively inactive.`,
		example: `  # Disable a Skyhook
  kubectl skyhook disable gpu-init

  # Disable without confirmation
  kubectl skyhook disable gpu-init --confirm`,
		annotation:   utils.DisableAnnotation,
		action:       "set",
		verb:         "disabled",
		confirmVerb:  "disable",
		needsConfirm: true,
	})
}

// NewEnableCmd creates the enable command
func NewEnableCmd(ctx *cliContext.CLIContext) *cobra.Command {
	return newLifecycleCmd(ctx, lifecycleConfig{
		use:   "enable <skyhook-name>",
		short: "Enable a disabled Skyhook",
		long: `Enable a disabled Skyhook by removing the disable annotation.

The operator will resume normal processing after this command.`,
		example: `  # Enable a disabled Skyhook
  kubectl skyhook enable gpu-init

  # Enable without confirmation
  kubectl skyhook enable gpu-init --confirm`,
		annotation:   utils.DisableAnnotation,
		action:       "remove",
		verb:         "enabled",
		confirmVerb:  "enable",
		needsConfirm: true,
	})
}
