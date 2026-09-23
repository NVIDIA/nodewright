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

package config

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/NVIDIA/nodewright/agent/internal/schema"
	"github.com/NVIDIA/nodewright/agent/internal/stage"
	"github.com/NVIDIA/nodewright/agent/internal/step"
)

// This file is the single home for config validation. It covers both layers a
// loaded document must pass: the JSON Schema check (SchemaValidator, below) and
// the cross-step document rules (validateModes, further down) that no single
// step can enforce on its own.

// schemaFS holds the authoritative JSON Schemas the agent validates package
// configs against. They are copied byte-for-byte from the legacy agent's
// schemas/ tree and embedded so the binary stays a single static artifact.
//
//go:embed schemas/v1/*.json
var schemaFS embed.FS

// rootSchemaFile is the per-version entrypoint schema; the others it pulls in
// via $ref are siblings in the same version directory.
const rootSchemaFile = "skyhook-agent-schema.json"

// schemaScheme is a synthetic URL scheme the compiler routes through
// embeddedLoader. The v1 schemas use relative $id/$ref values whose URI
// resolution would otherwise point at the filesystem; routing every load
// through our loader keeps resolution entirely inside the embed FS.
const schemaScheme = "skyhook"

// SchemaValidator validates a raw config document against the JSON Schema for a
// given version. Loader depends on this interface rather than a concrete
// validator so the backend can be substituted (for example, faked in tests).
type SchemaValidator interface {
	Validate(data []byte, v schema.SchemaVersion) error
}

// embeddedValidator validates documents against the JSON Schemas embedded in
// the binary. The schemas are tiny and single-version, so it compiles on
// demand and holds no state — there is no package-global cache to share.
type embeddedValidator struct{}

func newSchemaValidator() embeddedValidator { return embeddedValidator{} }

// Validate checks a raw config document against the embedded schema for v. An
// unrecognised version is reported as such (rather than as a parse failure).
func (embeddedValidator) Validate(data []byte, v schema.SchemaVersion) error {
	compiled, err := compileSchema(v)
	if err != nil {
		return err
	}
	// jsonschema.UnmarshalJSON decodes numbers as json.Number, which the
	// validator needs to honour the schema's integer/number distinctions
	// (e.g. returncodes); a plain json.Unmarshal into any would yield
	// float64 and weaken those checks.
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parsing config json: %w", err)
	}
	if err := compiled.Validate(doc); err != nil {
		return fmt.Errorf("config failed schema validation: %w", err)
	}
	return nil
}

// compileSchema compiles the version's root schema, resolving its cross-file
// $ref through embeddedLoader so every referenced document is read from the
// embed FS.
func compileSchema(v schema.SchemaVersion) (*jsonschema.Schema, error) {
	if _, err := schemaFS.ReadDir("schemas/" + string(v)); err != nil {
		return nil, fmt.Errorf("unknown schema version %q", v)
	}

	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{schemaScheme: embeddedLoader{version: v}})

	root := rootSchemaURL(v)
	compiled, err := c.Compile(root)
	if err != nil {
		return nil, fmt.Errorf("compiling schema %q: %w", root, err)
	}
	return compiled, nil
}

// rootSchemaURL is the synthetic absolute URL the root schema compiles under.
func rootSchemaURL(v schema.SchemaVersion) string {
	return schemaScheme + "://schemas/" + string(v) + "/" + rootSchemaFile
}

// embeddedLoader serves schema documents out of schemaFS for a single
// version. It keys off the URL's basename, so it is indifferent to whatever
// base URI the compiler computes when resolving the relative $ref.
type embeddedLoader struct{ version schema.SchemaVersion }

func (l embeddedLoader) Load(rawURL string) (any, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parsing schema url %q: %w", rawURL, err)
	}
	name := path.Base(u.Path)
	raw, err := schemaFS.ReadFile("schemas/" + string(l.version) + "/" + name)
	if err != nil {
		return nil, fmt.Errorf("loading embedded schema %q: %w", name, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parsing embedded schema %q: %w", name, err)
	}
	return doc, nil
}

// modeRule is a single cross-step invariant over the assembled modes.
type modeRule func(map[stage.Stage][]step.Step) error

// modeRules are the fatal cross-step invariants, in the order validateModes
// runs them. Order matters: placement is checked before pairing so a misplaced
// UpgradeStep in an otherwise-unchecked stage reports the specific placement
// error rather than the generic missing-checks one.
var modeRules = []modeRule{
	requireDefinedSteps,
	requireNonCheckStage,
	requireUpgradeStepPlacement,
	requireApplyChecks,
}

// validateModes enforces the cross-step rules a single Step cannot check on its
// own: that some work is defined, that every apply-style stage has matching
// checks, that UpgradeSteps sit only in the upgrade stages, and that every
// referenced step file exists under rootDir. A missing check counterpart for a
// step is a non-fatal warning emitted via logger (nil uses slog.Default()).
func validateModes(modes map[stage.Stage][]step.Step, rootDir string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	if err := checkModeRules(modes); err != nil {
		return err
	}

	warnMissingCheckPairs(modes, logger)

	return checkStepFilesExist(modes, rootDir)
}

// checkModeRules runs the fatal structural cross-step invariants — the ones
// that don't touch disk. Both Load (via validateModes) and Dump use it so a
// config can't serialize cleanly yet be rejected on reload.
func checkModeRules(modes map[stage.Stage][]step.Step) error {
	for _, rule := range modeRules {
		if err := rule(modes); err != nil {
			return err
		}
	}
	return nil
}

func requireDefinedSteps(modes map[stage.Stage][]step.Step) error {
	for _, list := range modes {
		if len(list) > 0 {
			return nil
		}
	}
	return errors.New("there are no defined steps")
}

func requireNonCheckStage(modes map[stage.Stage][]step.Step) error {
	for st := range stage.ApplyToCheck {
		// A declared-but-empty stage (e.g. "apply": []) does not count: it
		// carries no work, so it must not satisfy the "must define a
		// non-check stage" rule.
		if len(modes[st]) > 0 {
			return nil
		}
	}
	names := make([]string, 0, len(stage.ApplyToCheck))
	for st := range stage.ApplyToCheck {
		names = append(names, string(st))
	}
	sort.Strings(names)
	return fmt.Errorf("there are only check stages defined; define at least one of: %s", strings.Join(names, ", "))
}

func requireApplyChecks(modes map[stage.Stage][]step.Step) error {
	for applyStage, checkStage := range stage.ApplyToCheck {
		if len(modes[applyStage]) > 0 && len(modes[checkStage]) == 0 {
			return fmt.Errorf("stage %q has steps but no corresponding %q checks", applyStage, checkStage)
		}
	}
	return nil
}

func requireUpgradeStepPlacement(modes map[stage.Stage][]step.Step) error {
	for st, list := range modes {
		for _, s := range list {
			u, ok := s.(step.UpgradeStep)
			if !ok {
				continue
			}
			if st != stage.Upgrade && st != stage.UpgradeCheck {
				return fmt.Errorf(
					"UpgradeStep %q is defined in stage %q but may only appear in %q or %q",
					u.Path(), st, stage.Upgrade, stage.UpgradeCheck,
				)
			}
			if err := u.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// warnMissingCheckPairs warns when a step has no counterpart in its own check
// stage. The expected check name is the step path with "_check" inserted
// before the extension (or appended when there is none), e.g.
// foo.sh -> foo_check.sh. Each step is matched only against its paired check
// stage (stage.ApplyToCheck[stage]) so an identically named file in an unrelated
// check stage cannot mask a genuinely missing check.
func warnMissingCheckPairs(modes map[stage.Stage][]step.Step, logger *slog.Logger) {
	for st, list := range modes {
		checkStage, ok := stage.ApplyToCheck[st]
		if !ok {
			// Check stages and non-step stages (Interrupt) have no
			// apply->check pairing of their own.
			continue
		}

		checkPaths := make(map[string]struct{}, len(modes[checkStage]))
		for _, s := range modes[checkStage] {
			checkPaths[s.Path()] = struct{}{}
		}

		for _, s := range list {
			expected := expectedCheckName(s.Path())
			if _, ok := checkPaths[expected]; !ok {
				logger.Warn(
					"step has no corresponding check; checks ensure all tasks in the step complete",
					"step", s.Path(),
					"stage", string(st),
					"expectedCheck", expected,
				)
			}
		}
	}
}

func checkStepFilesExist(modes map[stage.Stage][]step.Step, rootDir string) error {
	var missing []string
	for _, list := range modes {
		for _, s := range list {
			rel := s.Path()
			// Reject absolute paths and ones that climb outside rootDir (and
			// the empty path): a step path must resolve inside the package
			// root the agent unpacks to, never escape it.
			if !filepath.IsLocal(rel) {
				return fmt.Errorf("step path %q must be relative to and contained within the package root", rel)
			}
			switch _, err := os.Stat(filepath.Join(rootDir, rel)); {
			case err == nil:
				// file present
			case os.IsNotExist(err):
				missing = append(missing, rel)
			default:
				// A stat failure that isn't "not found" (permission, I/O) is a
				// real error, not a missing step — surface it rather than
				// mislabeling the path as absent.
				return fmt.Errorf("checking step %q under %q: %w", rel, rootDir, err)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("the following steps do not exist: %s", strings.Join(missing, ", "))
	}
	return nil
}

// expectedCheckName derives a step's check counterpart by inserting "_check"
// before the extension of the file's basename (appended when there is none),
// e.g. foo.sh -> foo_check.sh. The directory is split off first so a dot in a
// parent directory (dir.v1/foo) doesn't get mistaken for an extension.
func expectedCheckName(p string) string {
	dir, base := path.Split(p)
	i := strings.LastIndex(base, ".")
	if i < 0 {
		return dir + base + "_check"
	}
	name, ext := base[:i], base[i+1:]
	if ext == "" {
		return dir + name + "_check"
	}
	return dir + name + "_check." + ext
}
