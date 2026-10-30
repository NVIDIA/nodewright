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

// The assertions in this file are deliberately NOT golden-driven. A golden file
// is repaired by regenerating it, and for a policy that is the exact failure
// mode the policy exists to prevent: someone refactors, runs -update, CI turns
// green, and the SBOM is back on the index. Everything here is a rule that must
// survive a refactor whose author was sure it was equivalent, so each one is
// written out with the reason it exists in its failure message.

package releasepolicy

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// TestSBOMAndVEXTargetAPlatformManifest pins the one decision the whole change
// exists to make. An SBOM describes exactly one root filesystem and a VEX claim
// is verifiable only when its product identifier binds to the manifest it
// covers, so both hang on a platform manifest. Moving either back onto the
// index republishes the bug, and it would look like a one-line simplification
// in review.
func TestSBOMAndVEXTargetAPlatformManifest(t *testing.T) {
	t.Parallel()
	ops := actionOps(loadAction(t, attestActionPath))
	attests := attestations(ops)
	if len(attests) == 0 {
		t.Fatalf("%s publishes no attestations at all", attestActionPath)
	}

	published := map[string]map[string]bool{}
	for _, op := range attests {
		switch {
		case op.Subject == indexDigestVar:
			t.Errorf(`%s step %q attests %s to %s.
SBOM and VEX evidence must hang on the platform manifest it describes, never on
the index: an SBOM on an index honestly describes neither child, and a consumer
who resolves linux/amd64 and enumerates referrers on that manifest finds nothing.
  %s`, attestActionPath, op.Step, op.Label, op.Subject, op.Command)
		case op.Subject != platformDigestsVar:
			t.Errorf(`%s step %q attests %s to %q, which is not each platform's digest (%s).
Every attestation subject has to resolve to a platform manifest digest; a subject
this policy cannot name is one nothing here can check.
  %s`, attestActionPath, op.Step, op.Label, op.Subject, platformDigestsVar, op.Command)
		}

		predicate := strings.TrimPrefix(op.Label, "cosign attest ")
		if predicate != "cyclonedx" && predicate != "openvex" {
			t.Errorf("%s step %q runs cosign attest with predicate type %q, which this policy does not know:\n  %s",
				attestActionPath, op.Step, predicate, op.Command)
			continue
		}
		if published[predicate] == nil {
			published[predicate] = map[string]bool{}
		}
		published[predicate][op.Subject] = true
	}

	// Both predicates on every platform. A refactor that dropped the openvex
	// attestation would still leave the assertions above passing on everything
	// that remained. That each listed platform really receives both is driven
	// through the real script in TestEveryPlatformGetsBothAttestations.
	for _, predicate := range []string{"cyclonedx", "openvex"} {
		if !published[predicate][platformDigestsVar] {
			t.Errorf(`no %s attestation targets each platform's digest (%s).
Each platform manifest carries its own SBOM and its own VEX document; a missing
one leaves a referrers listing that reads as complete while a platform ships
with no evidence of its own.`, predicate, platformDigestsVar)
		}
	}
}

// TestSignatureTargetsTheIndex covers the other side of the split. The index is
// what a user pulls, so signing a platform manifest instead would leave the
// pulled artifact unsigned while every check still reported a signature.
func TestSignatureTargetsTheIndex(t *testing.T) {
	t.Parallel()
	signs := signatures(actionOps(loadAction(t, attestActionPath)))
	if len(signs) != 1 {
		t.Fatalf("%s runs cosign sign %d time(s), want exactly 1 (the index)", attestActionPath, len(signs))
	}
	if signs[0].Subject != indexDigestVar {
		t.Errorf(`%s step %q signs %s rather than %s.
The index is what a user pulls, so the signature belongs on it; signing a
platform manifest leaves the pulled artifact unsigned.
  %s`, attestActionPath, signs[0].Step, signs[0].Subject, indexDigestVar, signs[0].Command)
	}
}

// TestDigestVariablesAreWiredToTheRightSource keeps the subject assertions from
// pinning variable names alone: a step could set PLATFORM_DIGESTS from the
// index input and still read as per-platform everywhere else in this package.
func TestDigestVariablesAreWiredToTheRightSource(t *testing.T) {
	t.Parallel()
	steps := loadAction(t, attestActionPath).Runs.Steps

	sign := steps.named(t, "Sign index")
	if got, want := sign.Env[indexDigestVar], "${{ inputs.index-digest }}"; got != want {
		t.Errorf("Sign index env %s = %q, want %q", indexDigestVar, got, want)
	}

	attest := steps.named(t, "Attest per-platform SBOM and VEX")
	for _, name := range []string{"Generate and validate per-platform evidence", "Attest per-platform SBOM and VEX"} {
		want := "${{ steps.platforms.outputs.platform-digests }}"
		if got := steps.named(t, name).Env[platformDigestsVar]; got != want {
			t.Errorf("%s env %s = %q, want %q (the resolved platform digests, not an input)",
				name, platformDigestsVar, got, want)
		}
	}
	if _, present := attest.Env[indexDigestVar]; present {
		t.Errorf("the attest step takes %s in its env; nothing it publishes belongs on the index, so the variable should not be reachable from it", indexDigestVar)
	}
}

// TestProvenanceStaysOnTheIndex pins the subject that did not move. SLSA
// provenance describes the build, not one root filesystem, so it stays on the
// index and stays with actions/attest-build-provenance in the calling workflow.
func TestProvenanceStaysOnTheIndex(t *testing.T) {
	t.Parallel()

	t.Run("the attest action does no provenance", func(t *testing.T) {
		t.Parallel()
		steps := loadAction(t, attestActionPath).Runs.Steps
		if used := steps.using("actions/attest-build-provenance"); len(used) > 0 {
			t.Error("cosign-attest-multiplatform runs attest-build-provenance; provenance belongs to the calling workflow, on the index")
		}
		for _, command := range commandsInvoking(steps, "cosign attest") {
			if strings.Contains(command, "slsa") || strings.Contains(command, "provenance") {
				t.Errorf("the attest action publishes provenance itself: %s", command)
			}
		}
	})

	for _, workflow := range []string{operatorCIPath, agentCIPath, cliReleasePath} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()
			steps := loadWorkflow(t, workflow).steps()

			attests := steps.using("./.github/actions/cosign-attest-multiplatform")
			if len(attests) != 1 {
				t.Fatalf("%s calls cosign-attest-multiplatform %d times, want 1", workflow, len(attests))
			}
			indexDigest := attests[0].With["index-digest"]
			if indexDigest == "" {
				t.Fatalf("%s does not pass index-digest to cosign-attest-multiplatform", workflow)
			}

			provenance := steps.using("actions/attest-build-provenance")
			if len(provenance) != 1 {
				t.Fatalf("%s runs attest-build-provenance %d times, want 1", workflow, len(provenance))
			}
			if got := provenance[0].With["subject-digest"]; got != indexDigest {
				t.Errorf(`%s attests provenance to %q while the index digest is %q.
SLSA provenance is about the build that produced the artifact, not about one
root filesystem, so it must stay on the index the user pulls.`, workflow, got, indexDigest)
			}
		})
	}
}

// TestEveryCosignCallPinsTheBundleFormat is asserted across every file that runs
// cosign, producing and consuming alike. The flag decides whether evidence
// publishes through the referrers API or a legacy .att tag, and its default has
// moved between cosign releases. A producer and a consumer that disagree do not
// error: the consumer looks in the wrong place and reports the evidence missing.
func TestEveryCosignCallPinsTheBundleFormat(t *testing.T) {
	t.Parallel()
	for _, path := range []string{attestActionPath, verifyActionPath, chartSignActionPath} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			steps := loadAction(t, path).Runs.Steps
			// Both verify forms are named explicitly. Matching is token-aware
			// from the executable position, so `cosign verify` no longer also
			// matches `cosign verify-attestation`; they are different commands
			// and each has to be swept on its own.
			var found int
			for _, program := range []string{"cosign sign", "cosign attest", "cosign verify", "cosign verify-attestation"} {
				for _, command := range commandsInvoking(steps, program) {
					found++
					if !hasFlag(command, "--new-bundle-format=true") {
						t.Errorf(`%s runs cosign without --new-bundle-format=true:
  %s
The flag decides whether evidence lands through the OCI referrers API or as a
legacy .att tag, and its default has moved between cosign releases. Signing and
verification disagreeing about it does not error, it reports the evidence as
missing.`, path, command)
					}
				}
			}
			if found == 0 {
				t.Errorf("no cosign invocation found in %s; this assertion is only meaningful if it has calls to check", path)
			}
		})
	}
}

// TestRegistryCallsAreBounded pins that no call to a registry or to Sigstore can
// hang a release job until the runner is killed. Verification is the worst case
// and gets its own subtest: it runs last, after the artifacts are pushed, which
// is the point at which a hang is least visible and most expensive.
func TestRegistryCallsAreBounded(t *testing.T) {
	t.Parallel()

	t.Run("publishing calls", func(t *testing.T) {
		t.Parallel()
		steps := loadAction(t, attestActionPath).Runs.Steps
		for _, program := range []string{"cosign sign", "cosign attest", "crane digest"} {
			commands := commandsInvoking(steps, program)
			if len(commands) == 0 {
				t.Errorf("no %s invocation found in %s", program, attestActionPath)
			}
			for _, command := range commands {
				if !timeoutWrapped(command, program) {
					t.Errorf(`%s is not wrapped in timeout:
  %s
An unbounded registry call hangs the release job until the runner is killed.`, program, command)
				}
			}
		}
	})

	t.Run("verification calls", func(t *testing.T) {
		t.Parallel()
		steps := loadAction(t, verifyActionPath).Runs.Steps
		for _, program := range []string{"cosign verify", "cosign verify-attestation"} {
			commands := commandsInvoking(steps, program)
			if len(commands) == 0 {
				t.Errorf("no %s invocation found in %s", program, verifyActionPath)
			}
			for _, command := range commands {
				if !timeoutWrapped(command, program) {
					t.Errorf(`%s is not wrapped in timeout:
  %s
Verification runs after the artifacts are pushed, so an unbounded call hangs the
release at its least recoverable point.`, program, command)
				}
			}
		}
	})
}

// TestCosignVersionPinsAgree is the assertion that exists because a human
// comment is otherwise the only thing holding the invariant together: three
// separate files repeat the same version literal and none reads it from
// another. All three matter, and the chart's is the one easiest to forget,
// because it is not part of the per-platform split and looks unrelated.
func TestCosignVersionPinsAgree(t *testing.T) {
	t.Parallel()
	attestPin := loadAction(t, attestActionPath).Inputs["cosign-version"].Default
	if attestPin == "" {
		t.Fatalf("%s declares no inputs.cosign-version.default", attestActionPath)
	}

	// The verify action covers both the image path and the chart path, so a
	// disagreement with either signing side fails a release at its last step,
	// after the artifact is already pushed.
	for _, path := range []string{verifyActionPath, chartSignActionPath} {
		installers := loadAction(t, path).Runs.Steps.using("sigstore/cosign-installer")
		if len(installers) != 1 {
			t.Fatalf("%s installs cosign %d times, want 1", path, len(installers))
		}
		pin := installers[0].With["cosign-release"]
		if pin == "" {
			t.Errorf(`%s installs cosign without pinning cosign-release.
Leaving it to the installer's default makes the cosign version an implicit dependency on the installer's SHA, which Renovate bumps routinely. A bump to an installer defaulting to a different cosign major would change the on-registry bundle layout under a verification step that pins %q explicitly.`,
				path, attestPin)
			continue
		}
		if pin != attestPin {
			t.Errorf(`cosign pins disagree: %s declares inputs.cosign-version.default = %q, %s installs cosign-release: %q.
A release must install one cosign version across sign, attest and verify: verification reads the on-registry layout that signing produced, and --new-bundle-format has to mean the same thing on both sides. Update every file together.`,
				attestActionPath, attestPin, path, pin)
		}
	}
}

// TestImageWorkflowsAttachNoEvidenceToTheIndex covers what the action alone
// cannot. The action can only attest where it is told to, so a workflow that
// reintroduced the single-subject action beside it would put an SBOM back on the
// index without touching the action at all.
func TestImageWorkflowsAttachNoEvidenceToTheIndex(t *testing.T) {
	t.Parallel()
	for _, workflow := range []string{operatorCIPath, agentCIPath, cliReleasePath} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()
			steps := loadWorkflow(t, workflow).steps()

			if used := steps.using("./.github/actions/cosign-sign-sbom"); len(used) > 0 {
				t.Errorf("%s uses cosign-sign-sbom, which attaches one SBOM to the subject it is given; a multi-platform image must use cosign-attest-multiplatform instead", workflow)
			}
			for _, step := range steps {
				for _, command := range shellCommands(step.Run) {
					if invokes(command, "cosign attest") || invokes(command, "cosign sign") {
						t.Errorf(`%s step %q signs or attests inline:
  %s
All image evidence goes through cosign-attest-multiplatform so the subject policy
lives in one place.`, workflow, step.Name, command)
					}
				}
			}
			if len(steps.using("./.github/actions/cosign-attest-multiplatform")) != 1 {
				t.Errorf("%s must call ./.github/actions/cosign-attest-multiplatform exactly once", workflow)
			}
		})
	}
}

// TestImageWorkflowWiring pins the seam between the two actions. The attest
// action resolves the platform digests, and only the workflow can carry them to
// verification; a dropped output would leave verification checking nothing while
// the job still reported success.
func TestImageWorkflowWiring(t *testing.T) {
	t.Parallel()
	for _, workflow := range []string{operatorCIPath, agentCIPath, cliReleasePath} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()
			steps := loadWorkflow(t, workflow).publishingJob(t).Job.Steps

			attests := steps.using("./.github/actions/cosign-attest-multiplatform")
			if len(attests) != 1 {
				t.Fatalf("%s calls cosign-attest-multiplatform %d times, want 1", workflow, len(attests))
			}
			attest := attests[0]
			if attest.With["index-digest"] == "" {
				t.Error("the attest step must be passed index-digest")
			}
			if attest.With["subject-name"] == "" {
				t.Error("the attest step must be passed subject-name")
			}
			if attest.ID == "" {
				t.Fatal("the attest step needs an id; its platform digest outputs are unreachable without one")
			}

			declared := steps.ids()
			verifies := steps.using("./.github/actions/cosign-verify-release")
			if len(verifies) != 1 {
				t.Fatalf("%s calls cosign-verify-release %d times, want 1", workflow, len(verifies))
			}
			const output = "platform-digests"
			want := "${{ steps." + attest.ID + ".outputs." + output + " }}"
			got := verifies[0].With[output]
			if got != want {
				t.Errorf(`%s verify step %s = %q, want %q (the attest step's output).
Verification has to follow the evidence to the platform manifests it was
published against; a digest that does not come from the attest step leaves
verification checking a subject nothing attested.`, workflow, output, got, want)
			}
			referenced := referencedStepID(got)
			if referenced == "" || !declared[referenced] {
				t.Errorf("%s verify step %s references step id %q, which no step in the publishing job declares", workflow, output, referenced)
			}
		})
	}
}

// TestMirroredImagesCarryTheirEvidence pins the NGC copy of each image and of
// the CLI. A mirror job signs nothing: it copies the published index together
// with its referrers and verifies the copy against the platform digests the
// publishing job attested. A copy without -r leaves the per-platform SBOM and VEX behind, since
// they hang on the platform manifests rather than the index, and a mirror that
// verifies digests from anywhere else checks a subject nothing attested.
func TestMirroredImagesCarryTheirEvidence(t *testing.T) {
	t.Parallel()
	for _, workflow := range []string{operatorCIPath, agentCIPath, cliReleasePath} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()
			loaded := loadWorkflow(t, workflow)
			publisher := loaded.publishingJob(t)
			attests := publisher.Job.Steps.using("./.github/actions/cosign-attest-multiplatform")
			if len(attests) != 1 || attests[0].ID == "" {
				t.Fatalf("%s job %s needs exactly one cosign-attest-multiplatform step with an id", workflow, publisher.ID)
			}

			var mirrors int
			for _, job := range loaded.Jobs {
				if job.ID == publisher.ID {
					continue
				}
				verifies := job.Job.Steps.using("./.github/actions/cosign-verify-release")
				if len(verifies) == 0 {
					continue
				}
				mirrors++
				if len(verifies) != 1 {
					t.Errorf("%s job %s calls cosign-verify-release %d times, want 1", workflow, job.ID, len(verifies))
				}
				if len(job.Job.Steps.using("actions/attest-build-provenance")) > 0 {
					t.Errorf("%s job %s attests provenance; a mirror copies the publishing job's evidence rather than producing its own", workflow, job.ID)
				}

				var destinations []string
				for _, command := range commandsInvoking(job.Job.Steps, "oras cp") {
					if hasFlag(command, "-r") {
						destinations = append(destinations, copyDestination(command))
					}
				}
				if len(destinations) == 0 {
					t.Errorf(`%s job %s verifies a copy it did not make with `+"`oras cp -r`"+`.
Without -r the copy leaves the signature, SBOM and VEX on the source registry, and
verification fails at the end of a release, after the index is already published.`, workflow, job.ID)
				}
				verified := verifies[0].With["subject-name"]
				if !slices.ContainsFunc(destinations, func(variable string) bool {
					return variable != "" && verified == "${{ env."+variable+" }}"
				}) {
					t.Errorf(`%s job %s verifies subject-name %q, which is not the repository its `+"`oras cp -r`"+` copied to.
Verifying any other repository passes against evidence that was never copied.`, workflow, job.ID, verified)
				}

				// The platform digests below are only valid for the copy if the copy is
				// the attested index, so the mirror has to compare the two index digests
				// before it verifies.
				subjectDigest := verifies[0].With["subject-digest"]
				resolved := referencedStepID(subjectDigest)
				if resolved == "" || !job.Job.Steps.ids()[resolved] {
					t.Errorf("%s job %s verify step subject-digest %q does not come from a step in the job", workflow, job.ID, subjectDigest)
				}
				published := "${{ needs." + publisher.ID + ".outputs.digest }}"
				if got, want := publisher.Job.Outputs["digest"], attests[0].With["index-digest"]; got != want {
					t.Errorf("%s job %s outputs digest = %q, want the attested index digest %q", workflow, publisher.ID, got, want)
				}
				if !slices.ContainsFunc(job.Job.Steps, func(step Step) bool {
					return slices.Contains(slices.Collect(maps.Values(step.Env)), subjectDigest) &&
						slices.Contains(slices.Collect(maps.Values(step.Env)), published)
				}) {
					t.Errorf(`%s job %s has no step comparing the copied index %s with the attested one %s.
Without it a copy that changed the index would be verified against platform
digests that belong to a different index.`, workflow, job.ID, subjectDigest, published)
				}

				const output = "platform-digests"
				got := verifies[0].With[output]
				prefix := "${{ needs." + publisher.ID + ".outputs."
				name, found := strings.CutPrefix(got, prefix)
				name, closed := strings.CutSuffix(name, " }}")
				want := "${{ steps." + attests[0].ID + ".outputs." + output + " }}"
				if !found || !closed || publisher.Job.Outputs[name] != want {
					t.Errorf(`%s job %s verify step %s = %q, want a %s job output bound to %q.
The copy is byte-identical to what the publishing job attested, so its platform
digests are the ones that job resolved; a digest from anywhere else leaves
verification checking a subject nothing attested.`, workflow, job.ID, output, got, publisher.ID, want)
				}
			}
			if mirrors == 0 {
				t.Errorf("%s has no job that copies the release to NGC and verifies the copy", workflow)
			}
		})
	}
}

// TestHelmChartKeepsSingleSubjectEvidence pins the deliberate exception. The
// chart is a single OCI artifact with no platform children, so per-platform
// evidence there would describe nothing, and the multi-platform action would
// fail closed trying to resolve children that do not exist.
func TestHelmChartKeepsSingleSubjectEvidence(t *testing.T) {
	t.Parallel()
	steps := loadWorkflow(t, releasePath).steps()

	if len(steps.using("./.github/actions/cosign-sign-sbom")) != 1 {
		t.Errorf("%s must keep signing the Helm chart with cosign-sign-sbom", releasePath)
	}
	if used := steps.using("./.github/actions/cosign-attest-multiplatform"); len(used) > 0 {
		t.Errorf("%s uses cosign-attest-multiplatform; the Helm chart has no platform children to split evidence across", releasePath)
	}

	verifies := steps.using("./.github/actions/cosign-verify-release")
	if len(verifies) != 1 {
		t.Fatalf("%s calls cosign-verify-release %d times, want 1", releasePath, len(verifies))
	}
	if value, present := verifies[0].With["platform-digests"]; present {
		t.Errorf(`%s passes platform-digests = %q to chart verification.
A chart has no platforms, so every check must run against the one subject; a
platform digest here would send verification looking for evidence on a manifest
that does not exist.`, releasePath, value)
	}
}

// referencedStepID pulls the step id out of a `${{ steps.<id>.outputs.<name> }}`
// expression, returning "" when the expression is not of that shape.
// copyDestination returns the shell variable naming the repository an
// `oras cp` command copies to, from a destination written as "${NAME}:tags".
// It reads the last such reference rather than the last field: the tag list
// is often a parameter expansion with spaces in it, such as ${TAGS// /,}.
func copyDestination(command string) string {
	start := strings.LastIndex(command, `"${`)
	if start < 0 {
		return ""
	}
	name, _, found := strings.Cut(command[start+len(`"${`):], "}:")
	if !found || strings.ContainsAny(name, " ${}") {
		return ""
	}
	return name
}

func referencedStepID(expression string) string {
	const prefix = "${{ steps."
	if !strings.HasPrefix(expression, prefix) {
		return ""
	}
	id, _, found := strings.Cut(expression[len(prefix):], ".")
	if !found {
		return ""
	}
	return id
}

// TestCLIEvidenceFollowsThePushedBinaries pins the two CLI inputs no other
// release has. The platforms attested must be the ones pushed, or a platform
// added to the build ships without evidence until someone edits a second list;
// and each SBOM must be generated from the directory that was pushed, or it
// describes a binary other than the one the platform manifest carries.
func TestCLIEvidenceFollowsThePushedBinaries(t *testing.T) {
	t.Parallel()
	steps := loadWorkflow(t, cliReleasePath).publishingJob(t).Job.Steps

	attests := steps.using("./.github/actions/cosign-attest-multiplatform")
	if len(attests) != 1 {
		t.Fatalf("%s calls cosign-attest-multiplatform %d times, want 1", cliReleasePath, len(attests))
	}
	attest := attests[0]

	pushID := referencedStepID(attest.With["subject-name"])
	if pushID == "" {
		t.Fatalf("the CLI attest step's subject-name %q does not come from a step output", attest.With["subject-name"])
	}
	push := Step{}
	for _, step := range steps {
		if step.ID == pushID {
			push = step
		}
	}
	if push.ID == "" {
		t.Fatalf("no step in the CLI publishing job has id %q", pushID)
	}

	if got, want := attest.With["platforms"], "${{ steps."+pushID+".outputs.platforms }}"; got != want {
		t.Errorf(`CLI attest step platforms = %q, want %q.
The platforms attested have to be the platforms pushed; a separate list lets a
new platform publish with no SBOM and no VEX.`, got, want)
	}
	if got := attest.With["binaries-dir"]; got == "" || got != push.Env["BINARIES"] {
		t.Errorf(`CLI attest step binaries-dir = %q, want the directory the push step pushed from (%q).
An SBOM generated from any other directory describes a binary the platform
manifest does not carry.`, got, push.Env["BINARIES"])
	}
}
