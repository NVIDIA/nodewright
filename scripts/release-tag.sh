#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Interactive release-tag helper. Prompts for a component and a bump
# (major/minor/patch), optionally a release candidate, computes the next version,
# creates the git tag, and (after a separate explicit confirmation) pushes it.
#
# Pushing a <component>/v* tag triggers the CI release workflow, so the push is
# always a distinct, opt-in step. RC tags use the dotted `-rc.N` form so version
# sort orders pre-releases correctly (see docs/contributing/release-process.md).
#
# The tag is created on the current HEAD. Make sure the release commit (chart
# bump, CHANGELOG cut, etc.) is already committed and checked out before tagging.
# Both the tag creation and the push are gated behind explicit y/N confirms.

set -euo pipefail

cd "$(dirname "$0")/.."

# Colorize output only when stdout is a terminal and NO_COLOR is unset.
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    C_RESET=$'\033[0m' C_BOLD=$'\033[1m' C_DIM=$'\033[2m'
    C_CYAN=$'\033[36m' C_GREEN=$'\033[32m' C_YELLOW=$'\033[33m' C_RED=$'\033[31m'
else
    C_RESET='' C_BOLD='' C_DIM='' C_CYAN='' C_GREEN='' C_YELLOW='' C_RED=''
fi

if [[ ! -t 0 ]]; then
    echo "${C_RED}ERROR: release-tag.sh is interactive; run it from a terminal.${C_RESET}" >&2
    exit 1
fi

# Bump a vX.Y.Z version. Echoes the bumped vX.Y.Z.
bump_version() {
    local ver="${1#v}" kind="$2" major minor patch
    IFS=. read -r major minor patch <<<"$ver"
    case "$kind" in
        major) printf 'v%d.0.0\n' "$((major + 1))" ;;
        minor) printf 'v%d.%d.0\n' "$major" "$((minor + 1))" ;;
        patch) printf 'v%d.%d.%d\n' "$major" "$minor" "$((patch + 1))" ;;
        *)
            echo "ERROR: bump_version: unknown kind '$kind'" >&2
            return 1
            ;;
    esac
}

# Next RC number for a base version: one greater than the highest existing
# <component>/<base>-rc.<n>, or 1 if none exist.
next_rc() {
    local component="$1" base="$2" highest
    highest=$(git tag -l "${component}/${base}-rc.*" |
        sed -E "s#^${component}/${base}-rc\.##" | sort -n | tail -1)
    printf '%d\n' "$(((${highest:-0}) + 1))"
}

# Force one menu item per line: `select` otherwise packs items into columns
# by $COLUMNS, and the embedded ANSI codes throw off its width math.
COLUMNS=1
PS3="${C_BOLD}${C_CYAN}Select component: ${C_RESET}"
select COMPONENT in operator agent chart cli; do
    [[ -n "$COMPONENT" ]] && break
    echo "${C_RED}invalid selection${C_RESET}" >&2
done

LATEST_FINAL=$(git tag -l "${COMPONENT}/v*" --sort=-v:refname |
    grep -E "^${COMPONENT}/v[0-9]+\.[0-9]+\.[0-9]+$" | head -1)
LATEST_FINAL="${LATEST_FINAL#"${COMPONENT}/"}"
if [[ -z "$LATEST_FINAL" ]]; then
    echo "${C_RED}ERROR: no final ${COMPONENT}/vX.Y.Z tags found${C_RESET}" >&2
    exit 1
fi
echo "${C_DIM}Latest final tag:${C_RESET} ${C_BOLD}${COMPONENT}/${LATEST_FINAL}${C_RESET}"

MAJOR_NEXT=$(bump_version "$LATEST_FINAL" major)
MINOR_NEXT=$(bump_version "$LATEST_FINAL" minor)
PATCH_NEXT=$(bump_version "$LATEST_FINAL" patch)

# Menu text is colorized, so dispatch on the numeric $REPLY rather than the
# captured (escape-laden) $BUMP string.
PS3="${C_BOLD}${C_CYAN}Select bump: ${C_RESET}"
select BUMP in \
    "Major (${LATEST_FINAL} ${C_DIM}->${C_RESET} ${C_GREEN}${MAJOR_NEXT}${C_RESET})" \
    "Minor (${LATEST_FINAL} ${C_DIM}->${C_RESET} ${C_GREEN}${MINOR_NEXT}${C_RESET})" \
    "Patch (${LATEST_FINAL} ${C_DIM}->${C_RESET} ${C_GREEN}${PATCH_NEXT}${C_RESET})"; do
    [[ -n "$BUMP" ]] && break
    echo "${C_RED}invalid selection${C_RESET}" >&2
done

case "$REPLY" in
    1) BASE="$MAJOR_NEXT" ;;
    2) BASE="$MINOR_NEXT" ;;
    3) BASE="$PATCH_NEXT" ;;
esac

read -r -p "${C_YELLOW}Release candidate?${C_RESET} [y/N] " is_rc
if [[ "$is_rc" =~ ^[yY]$ ]]; then
    RC=$(next_rc "$COMPONENT" "$BASE")
    VERSION="${BASE}-rc.${RC}"
else
    VERSION="$BASE"
fi
TAG="${COMPONENT}/${VERSION}"

if git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then
    echo "${C_RED}ERROR: tag ${TAG} already exists${C_RESET}" >&2
    exit 1
fi

# Warn when RELEASE_NOTES.md still holds unpromoted notes. `make changelog`'s
# release cut promotes `## Unreleased` to the version heading the release
# workflows extract; tagging without having run it strands those notes on a
# heading nothing matches, and the workflow treats "no entry" as normal, so the
# miss is otherwise invisible until someone reads the release page (#495).
# The path mapping is duplicated from gen-changelog.sh because the CLI's is
# irregular (operator/cmd/cli, not cli/) -- as bump_version() already is between
# these two siblings.
case "$COMPONENT" in
    cli) NOTES="operator/cmd/cli/RELEASE_NOTES.md" ;;
    *) NOTES="${COMPONENT}/RELEASE_NOTES.md" ;;
esac

# Finals only. An RC is tagged mid-stabilization, before the release cut that
# promotes the heading, so `## Unreleased` is legitimately non-empty then and
# warning on every RC would just train people to ignore this.
if [[ "$VERSION" == "$BASE" && -f "$NOTES" ]]; then
    UNPROMOTED=$(awk '/^## / { f = ($0 ~ /^## Unreleased[[:space:]]*$/); next } f' "$NOTES")
    if [[ -n "${UNPROMOTED//[[:space:]]/}" ]] &&
        ! grep -qE "^## ${TAG//./\\.}([[:space:]]|$)" "$NOTES"; then
        echo
        echo "${C_YELLOW}WARNING: ${NOTES} has content under '## Unreleased' and no '## ${TAG}' section.${C_RESET}"
        echo "${C_YELLOW}         Those notes will NOT appear on the ${TAG} release page.${C_RESET}"
        echo "${C_DIM}         Cut the release with 'make changelog' (which promotes the heading),${C_RESET}"
        echo "${C_DIM}         commit, then re-run this.${C_RESET}"
    fi
fi

HEAD_REF="$(git rev-parse --abbrev-ref HEAD) @ $(git rev-parse --short HEAD)"

# Tags live on their component's release branch family: agent tags on
# release/agent/vX.Y.x, operator and chart tags on release/vX.Y.x, nothing on
# main (docs/contributing/release-process.md, Release Branch Strategy). Warn
# rather than refuse: the branch may legitimately be checked out under another
# name, and the person tagging can see the HEAD line below.
HEAD_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
case "$COMPONENT" in
    agent) EXPECTED_FAMILY="release/agent/" ;;
    *) EXPECTED_FAMILY="release/" ;;
esac
if [[ "$HEAD_BRANCH" != "${EXPECTED_FAMILY}"* ]] || [[ "$COMPONENT" != agent && "$HEAD_BRANCH" == release/agent/* ]]; then
    echo
    echo "${C_YELLOW}WARNING: HEAD is '${HEAD_BRANCH}'; ${COMPONENT} tags are expected on a ${EXPECTED_FAMILY}vX.Y.x branch.${C_RESET}"
fi

echo
echo "About to tag (on ${C_BOLD}${HEAD_REF}${C_RESET}): ${C_BOLD}${C_GREEN}${TAG}${C_RESET}"
echo "${C_DIM}Reminder: the release commit (chart bump, CHANGELOG) should already be committed at HEAD.${C_RESET}"
read -r -p "${C_YELLOW}Create local tag ${C_BOLD}${TAG}${C_RESET}${C_YELLOW}?${C_RESET} [y/N] " ok
if [[ ! "$ok" =~ ^[yY]$ ]]; then
    echo "${C_YELLOW}aborted${C_RESET}"
    exit 0
fi
git tag "$TAG"
echo "${C_GREEN}Created local tag ${TAG}.${C_RESET}"

echo
echo "${C_BOLD}${C_RED}Pushing ${TAG} will TRIGGER THE CI RELEASE for this component.${C_RESET}"
read -r -p "${C_YELLOW}Push ${C_BOLD}${TAG}${C_RESET}${C_YELLOW} to origin now?${C_RESET} [y/N] " push_ok
if [[ "$push_ok" =~ ^[yY]$ ]]; then
    git push origin "$TAG"
    echo "${C_GREEN}Pushed ${TAG}.${C_RESET}"
else
    echo "${C_DIM}Not pushed. Push later with: git push origin ${TAG}${C_RESET}"
fi
