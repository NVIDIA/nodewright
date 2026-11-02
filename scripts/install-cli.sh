#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Install the NodeWright CLI (kubectl-nodewright) from its OCI artifact,
# ghcr.io/nvidia/nodewright/cli.
#
#   curl -sSfL https://raw.githubusercontent.com/NVIDIA/nodewright/main/scripts/install-cli.sh | sh
#   curl -sSfL https://raw.githubusercontent.com/NVIDIA/nodewright/main/scripts/install-cli.sh | sh -s -- --version v0.3.0
#
# It needs only curl, awk and a sha256 tool: the registry is read over its HTTP
# API, so neither oras nor Docker is required. The downloaded binary is checked
# against the digest its manifest records, and when cosign is installed the
# release signature on the index is verified before anything is installed.
#
# Options:
#   --version <v>   Release to install, e.g. v0.3.0. Default: latest, the newest
#                   stable release.
#   --dir <path>    Install directory. Default: $HOME/.local/bin.
#   --no-verify     Skip the cosign signature check even when cosign is present.
#
# Linux and macOS only. On Windows, download the release zip from
# https://github.com/NVIDIA/nodewright/releases, or run
# `oras pull --platform windows/amd64 ghcr.io/nvidia/nodewright/cli:<version>`.

set -eu

REPOSITORY="${NODEWRIGHT_CLI_REPOSITORY:-ghcr.io/nvidia/nodewright/cli}"
IDENTITY='^https://github.com/NVIDIA/nodewright/\.github/workflows/cli-release\.yaml@refs/tags/cli/.*$'
ISSUER='https://token.actions.githubusercontent.com'

version=latest
dir="${HOME}/.local/bin"
verify=true

die() {
	printf 'install-cli: %s\n' "$*" >&2
	exit 1
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--version)
		[ "$#" -ge 2 ] || die "--version needs a value"
		version="$2"
		shift 2
		;;
	--dir)
		[ "$#" -ge 2 ] || die "--dir needs a value"
		dir="$2"
		shift 2
		;;
	--no-verify)
		verify=false
		shift
		;;
	-h | --help)
		# Not read from $0: under `curl ... | sh` there is no script file.
		cat <<'EOF'
Install kubectl-nodewright from ghcr.io/nvidia/nodewright/cli.

  --version <v>   Release to install, e.g. v0.3.0 (default: latest)
  --dir <path>    Install directory (default: $HOME/.local/bin)
  --no-verify     Skip the cosign signature check
EOF
		exit 0
		;;
	*) die "unknown option: $1" ;;
	esac
done

case "${version}" in
latest | v[0-9]*.[0-9]*.[0-9]*) ;;
*) die "version '${version}' is not 'latest' or v<major>.<minor>.<patch>" ;;
esac

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS $(uname -s); see the header of this script for Windows" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "sha256sum or shasum is required"
fi

host="${REPOSITORY%%/*}"
name="${REPOSITORY#*/}"
scheme=https
case "${host}" in
localhost:* | 127.0.0.1:*) scheme=http ;;
esac
api="${scheme}://${host}/v2/${name}"

work="$(mktemp -d "${TMPDIR:-/tmp}/install-cli.XXXXXX")"
trap 'rm -rf "${work}" ${staged:+"${staged}"}' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# GHCR serves public packages to an anonymous bearer token. A registry that
# needs none (a local test registry) answers the token request with an error,
# and the reads below go out without an Authorization header. The error is kept
# for the message below, since a refused token is also how a private or missing
# package first shows up.
token="$(curl -sSf "${scheme}://${host}/token?scope=repository:${name}:pull" 2>"${work}/token.err" |
	sed -n 's/.*"token":"\([^"]*\)".*/\1/p')" || token=""

fetch() { # url output accept
	if [ -n "${token}" ]; then
		curl -sSfL --retry 3 -H "Authorization: Bearer ${token}" -H "Accept: $3" -o "$2" "$1"
	else
		curl -sSfL --retry 3 -H "Accept: $3" -o "$2" "$1"
	fi
}

# Every object is fetched by digest after the index, and each is checked
# against the digest it was fetched by, so what is installed is exactly what
# the signed index names.
check() { # file digest
	actual="sha256:$(sha256 "$1")"
	[ "${actual}" = "$2" ] || die "$1 has digest ${actual}, expected $2"
}

# One JSON object per line, whitespace removed, so awk can read the fields
# without a JSON parser. The documents are the ones this project publishes.
flatten() {
	tr -d ' \t\r\n' <"$1" | tr '{' '\n'
}

if ! fetch "${api}/manifests/${version}" "${work}/index.json" \
	"application/vnd.oci.image.index.v1+json"; then
	[ -s "${work}/token.err" ] && printf 'install-cli: token request: %s\n' "$(cat "${work}/token.err")" >&2
	die "could not read ${REPOSITORY}:${version}. Releases from before the CLI moved to OCI exist only as archives; for those, or if the registry is unreachable, see https://github.com/NVIDIA/nodewright/releases"
fi
index_digest="sha256:$(sha256 "${work}/index.json")"

# An index entry flattens to two lines: the descriptor carrying the digest,
# then its platform object.
platform_digest="$(flatten "${work}/index.json" | awk -v os="${os}" -v arch="${arch}" '
	match($0, /"digest":"sha256:[a-f0-9]+"/) { last = substr($0, RSTART + 10, RLENGTH - 11) }
	index($0, "\"os\":\"" os "\"") && index($0, "\"architecture\":\"" arch "\"") { print last; exit }
')"
[ -n "${platform_digest}" ] || die "${REPOSITORY}:${version} has no ${os}/${arch} build"

if [ "${verify}" = true ]; then
	if command -v cosign >/dev/null 2>&1; then
		# The release is signed in the new bundle format. cosign v3 reads it by
		# default; cosign v2 looks for the legacy signature tag unless told, and
		# would report a validly signed release as unsigned.
		cosign verify \
			--certificate-identity-regexp "${IDENTITY}" \
			--certificate-oidc-issuer "${ISSUER}" \
			--new-bundle-format=true \
			"${REPOSITORY}@${index_digest}" >/dev/null ||
			die "signature verification failed for ${REPOSITORY}@${index_digest} (cosign v2.4 or later is needed; rerun with --no-verify to skip)"
		echo "Verified the release signature on ${REPOSITORY}@${index_digest}"
	else
		echo "cosign not found; skipping signature verification. To verify:"
		echo "  cosign verify --certificate-identity-regexp '${IDENTITY}' --certificate-oidc-issuer '${ISSUER}' --new-bundle-format=true ${REPOSITORY}@${index_digest}"
	fi
fi

fetch "${api}/manifests/${platform_digest}" "${work}/manifest.json" \
	"application/vnd.oci.image.manifest.v1+json"
check "${work}/manifest.json" "${platform_digest}"

# The artifact manifest has exactly one layer, the binary.
layer_digest="$(flatten "${work}/manifest.json" | awk '
	/"layers":\[$/ { layers = 1; next }
	layers && match($0, /"digest":"sha256:[a-f0-9]+"/) { print substr($0, RSTART + 10, RLENGTH - 11); exit }
')"
[ -n "${layer_digest}" ] || die "no binary layer in ${REPOSITORY}@${platform_digest}"

fetch "${api}/blobs/${layer_digest}" "${work}/kubectl-nodewright" "application/octet-stream"
check "${work}/kubectl-nodewright" "${layer_digest}"

# Staged next to the destination and renamed over it, so an interrupted copy
# never leaves a truncated kubectl-nodewright on PATH.
mkdir -p "${dir}"
staged="${dir}/.kubectl-nodewright.$$"
cp "${work}/kubectl-nodewright" "${staged}"
chmod 0755 "${staged}"
mv -f "${staged}" "${dir}/kubectl-nodewright"
echo "Installed kubectl-nodewright ${version} (${os}/${arch}) to ${dir}"

case ":${PATH}:" in
*":${dir}:"*) ;;
*) echo "Add ${dir} to your PATH so kubectl can find the plugin." ;;
esac
