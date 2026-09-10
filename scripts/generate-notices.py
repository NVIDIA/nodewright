#!/usr/bin/env python3

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


"""Generate THIRD_PARTY_NOTICES.md for Skyhook (operator, agent, root rollup)."""

import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
OPERATOR_DIR = REPO_ROOT / "operator"
AGENT_GO_DIR = REPO_ROOT / "agent" / "go"
AGENT_VENDOR = REPO_ROOT / "agent" / "vendor"
NOTICES_VENV = REPO_ROOT / "agent" / ".notices-venv"
LICENSES_CACHE = REPO_ROOT / ".licenses-cache"
OPERATOR_FILE = REPO_ROOT / "operator" / "THIRD_PARTY_NOTICES.md"
AGENT_FILE = REPO_ROOT / "agent" / "THIRD_PARTY_NOTICES.md"
ROOT_FILE = REPO_ROOT / "THIRD_PARTY_NOTICES.md"

# Platforms the released Go binaries are built for. The operator image is
# linux-only, but the CLI ships linux/darwin/windows x amd64/arm64 (see
# `cli-release-build` in operator/Makefile), and build-tagged sources pull
# different transitive dependencies per platform: github.com/Azure/go-ansiterm
# and github.com/inconshreveable/mousetrap are linked only on windows. Notices
# are therefore collected per platform and unioned.
GO_PLATFORMS = (
    ("linux", "amd64"),
    ("linux", "arm64"),
    ("darwin", "amd64"),
    ("darwin", "arm64"),
    ("windows", "amd64"),
    ("windows", "arm64"),
)


def tag(prefix: str) -> str:
    r = subprocess.run(
        ["git", "-C", str(REPO_ROOT), "describe", "--tags", "--abbrev=0", "--match", f"{prefix}/v*"],
        capture_output=True, text=True, check=False,
    )
    return r.stdout.strip() if r.returncode == 0 and r.stdout.strip() else "unreleased"


def _collapse_blanks(text: str) -> str:
    """Collapse runs of 3+ newlines to exactly 2 (one blank line)."""
    return re.sub(r"\n{3,}", "\n\n", text)


def go_env(goos: str = "", goarch: str = "") -> dict:
    env = {**os.environ, "GOFLAGS": "-mod=vendor"}
    if goos:
        env["GOOS"] = goos
    if goarch:
        env["GOARCH"] = goarch
    return env


def go_out(args: list[str], goos: str = "", goarch: str = "") -> str:
    return subprocess.check_output(["go", *args], cwd=OPERATOR_DIR, env=go_env(goos, goarch), text=True)


def license_files(pkg: str, cache_dirs) -> list:
    """License files saved for `pkg`, merged across the per-platform caches.

    A package present on only some platforms is saved only under those
    platforms' caches, so every cache has to be consulted. Files are keyed by
    name and the first cache that has one wins: all platforms copy the same
    file out of the same vendor tree, so a later duplicate carries no new
    information.
    """
    by_name = {}
    for d in cache_dirs:
        pkg_dir = d / pkg
        if not pkg_dir.is_dir():
            continue
        for f in sorted(p for p in pkg_dir.iterdir() if p.is_file()):
            by_name.setdefault(f.name, f)
    return [by_name[n] for n in sorted(by_name)]


def uncovered_modules(index_keys, pkg_to_module, linked_modules, local_module):
    """Linked modules that no index entry vouches for.

    Coverage is compared on module identity, never on path prefixes. Prefix
    matching lets an ancestor entry vouch for a nested submodule, and a nested
    submodule is a separate module whose license may differ from, or be missing
    in, its parent's. It also lets the bare `github.com` row that go-licenses
    emits when it cannot resolve a package match everything under that host,
    which would make this check vacuous.
    """
    known_modules = set(pkg_to_module.values())
    covered = set()
    for key in index_keys:
        # go-licenses keys a row on the directory a license covers, which is
        # often a roll-up prefix ("k8s.io/apimachinery/pkg") rather than a real
        # import path, so an exact lookup is not enough. Walking up finds the
        # longest known module containing the key, which is its owning module:
        # a key inside a nested submodule resolves to that submodule and never
        # to its parent. A key under no known module resolves to nothing.
        p = key
        while True:
            if p in pkg_to_module:
                covered.add(pkg_to_module[p])
                break
            if p in known_modules:
                covered.add(p)
                break
            if "/" not in p:
                break
            p = p.rsplit("/", 1)[0]
    return sorted(
        m for m in linked_modules
        if m
        and m != local_module
        and not m.startswith(f"{local_module}/")
        and m not in covered
    )


def operator_notices():
    gl = OPERATOR_DIR / "bin" / "go-licenses"
    gl_path = str(gl) if gl.exists() else (shutil.which("go-licenses") or "")
    if not gl_path:
        sys.exit("ERROR: go-licenses not found. Run 'make -C operator go-licenses'.")
    local = go_out(["list", "-m"]).strip()

    if LICENSES_CACHE.is_dir():
        shutil.rmtree(LICENSES_CACHE)
    elif LICENSES_CACHE.exists():
        LICENSES_CACHE.unlink()

    rows_set: set[tuple[str, str, str]] = set()
    cache_dirs: list[Path] = []
    linked: set[str] = set()
    pkg_to_module: dict[str, str] = {}
    for goos, goarch in GO_PLATFORMS:
        stdlib = go_out(["list", "std"], goos, goarch)
        ignore = ",".join([*sorted(line for line in stdlib.splitlines() if line), local])
        env = go_env(goos, goarch)
        cache_dir = LICENSES_CACHE / f"{goos}_{goarch}"
        cache_dirs.append(cache_dir)
        subprocess.run(
            [gl_path, "save", "./...", f"--save_path={cache_dir}", "--force", f"--ignore={ignore}"],
            cwd=OPERATOR_DIR, env=env, check=True,
        )
        csv = subprocess.check_output(
            [gl_path, "csv", "./...", f"--ignore={ignore}"], cwd=OPERATOR_DIR, env=env, text=True
        )
        rows_set |= {tuple(line.split(",", 2)) for line in csv.splitlines() if line.strip()}

        linked.update(
            m for m in go_out(
                ["list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./cmd/..."],
                goos, goarch,
            ).split()
        )
        for line in go_out(
            ["list", "-deps", "-f", "{{.ImportPath}} {{if .Module}}{{.Module.Path}}{{end}}", "./..."],
            goos, goarch,
        ).splitlines():
            parts = line.split()
            if len(parts) == 2:
                pkg_to_module[parts[0]] = parts[1]

    rows = sorted(rows_set)
    if not rows:
        sys.exit("ERROR: go-licenses produced no entries.")
    if not linked:
        sys.exit("ERROR: 'go list -deps ./cmd/...' resolved no modules; cannot verify license coverage.")
    if not pkg_to_module:
        sys.exit("ERROR: 'go list -deps ./...' produced no package-to-module map; cannot verify license coverage.")

    missing = uncovered_modules({pkg for pkg, _, _ in rows}, pkg_to_module, linked, local)
    if missing:
        sys.exit(
            "ERROR: module(s) linked into the released binaries have no license entry:\n"
            + "\n".join(f"  {m}" for m in missing)
            + "\n\nThese are redistributed, so their licenses must be disclosed in "
            + f"{OPERATOR_FILE.relative_to(REPO_ROOT)}.\n"
            + "Do not silence this by widening --ignore: that is what hid them previously."
        )

    out = [
        "# Third-Party Notices — Skyhook Operator + CLI",
        "",
        f"Operator tag: `{tag('operator')}`",
        "",
        "## Index",
        "",
        "| Package | License | Source |",
        "|---|---|---|",
    ]
    for pkg, url, lic in rows:
        out.append(f"| `{pkg}` | {lic or 'Unknown'} | {url or 'n/a'} |")
    out += ["", "## License Texts", ""]
    textless: list[str] = []
    for pkg, url, lic in rows:
        out += [f"### {pkg}", "", f"* License: {lic or 'Unknown'}", f"* Source: {url or 'n/a'}", ""]
        files = license_files(pkg, cache_dirs)
        if not files:
            textless.append(pkg)
        for f in files:
            out += [f"#### {f.name}", "", "```text", f.read_text(errors="replace").rstrip(), "```", ""]
    if textless:
        sys.exit(
            "ERROR: go-licenses reported a license for these packages but saved no license text:\n"
            + "\n".join(f"  {t}" for t in textless)
            + "\n\nAn index row without the text it names discloses nothing. This usually means\n"
            + "the per-platform license caches were not merged, so a package present on only\n"
            + "some platforms lost its text."
        )
    OPERATOR_FILE.write_text(_collapse_blanks("\n".join(out)) + "\n")
    print(f"Wrote {OPERATOR_FILE.relative_to(REPO_ROOT)} ({len(rows)} Go deps)", file=sys.stderr)


def _agent_python_notices():
    deps = []
    if AGENT_VENDOR.exists():
        for d in sorted(AGENT_VENDOR.iterdir()):
            if d.is_dir() and "-" in d.name:
                name, _, version = d.name.rpartition("-")
                deps.append((name, version))
    if not deps:
        return ["_No vendored Python dependencies found._", ""], 0

    if not (NOTICES_VENV / "bin" / "pip").exists():
        subprocess.run(["python3", "-m", "venv", str(NOTICES_VENV)], check=True)
    pip = str(NOTICES_VENV / "bin" / "pip")
    pip_licenses = str(NOTICES_VENV / "bin" / "pip-licenses")
    subprocess.run([pip, "install", "--quiet", "--upgrade", "pip", "pip-licenses"], check=True)
    subprocess.run(
        [pip, "install", "--quiet", "--upgrade", *[f"{n}=={v}" for n, v in deps]],
        check=True,
    )
    raw = subprocess.check_output(
        [pip_licenses, "--packages", *[n for n, _ in deps],
         "--with-license-file", "--with-urls", "--format=json", "--no-license-path"],
        text=True,
    )
    entries = sorted(json.loads(raw), key=lambda e: e["Name"].lower())

    def src(e) -> str:
        u = e.get("URL") or ""
        return u if u and u != "UNKNOWN" else "n/a"

    out = ["| Package | Version | License | Source |", "|---|---|---|---|"]
    for e in entries:
        out.append(f"| `{e['Name']}` | {e['Version']} | {e.get('License') or 'Unknown'} | {src(e)} |")
    out += ["", "### License Texts", ""]
    for e in entries:
        out += [
            f"#### {e['Name']} {e['Version']}", "",
            f"* License: {e.get('License') or 'Unknown'}",
            f"* Source: {src(e)}", "",
        ]
        text = (e.get("LicenseText") or "").strip()
        if text:
            out += ["```text", text, "```", ""]
        else:
            out += ["License text unavailable. See upstream source for the full license.", ""]
    return out, len(entries)


def _agent_go_notices():
    gl = AGENT_GO_DIR / "bin" / "go-licenses"
    gl_path = str(gl) if gl.exists() else (shutil.which("go-licenses") or "")
    if not gl_path:
        sys.exit("ERROR: go-licenses not found. Run 'make -C agent/go go-licenses'.")

    def go_out(args: list[str]) -> str:
        return subprocess.check_output(["go", *args], cwd=AGENT_GO_DIR, env=os.environ, text=True)

    local = go_out(["list", "-m"]).strip()
    stdlib = go_out(["list", "std"])
    ignore = ",".join([*sorted(line for line in stdlib.splitlines() if line), local])

    cache_dir = LICENSES_CACHE / "agent-go"
    if cache_dir.is_dir():
        shutil.rmtree(cache_dir)
    elif cache_dir.exists():
        cache_dir.unlink()
    subprocess.run(
        [gl_path, "save", "./...", f"--save_path={cache_dir}", "--force", f"--ignore={ignore}"],
        cwd=AGENT_GO_DIR, env=os.environ, check=True,
    )
    csv = subprocess.check_output(
        [gl_path, "csv", "./...", f"--ignore={ignore}"], cwd=AGENT_GO_DIR, env=os.environ, text=True
    )
    rows = sorted({tuple(line.split(",", 2)) for line in csv.splitlines() if line.strip()})
    if not rows:
        sys.exit("ERROR: go-licenses produced no entries for agent/go.")

    linked = set(
        m for m in go_out(
            ["list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./cmd/..."]
        ).split()
    )
    if not linked:
        sys.exit(
            "ERROR: 'go list -deps ./cmd/...' resolved no modules for agent/go; "
            "cannot verify license coverage."
        )

    pkg_to_module: dict[str, str] = {}
    for line in go_out(
        ["list", "-deps", "-f", "{{.ImportPath}} {{if .Module}}{{.Module.Path}}{{end}}", "./..."]
    ).splitlines():
        parts = line.split()
        if len(parts) == 2:
            pkg_to_module[parts[0]] = parts[1]
    if not pkg_to_module:
        sys.exit(
            "ERROR: 'go list -deps ./...' produced no package-to-module map for agent/go; "
            "cannot verify license coverage."
        )

    missing = uncovered_modules({pkg for pkg, _, _ in rows}, pkg_to_module, linked, local)
    if missing:
        sys.exit(
            "ERROR: module(s) linked into the released agent binary have no license entry:\n"
            + "\n".join(f"  {m}" for m in missing)
            + "\n\nThese are redistributed, so their licenses must be disclosed in "
            + f"{AGENT_FILE.relative_to(REPO_ROOT)}.\n"
            + "Do not silence this by widening --ignore: that is what hid them previously."
        )

    out = ["| Package | License | Source |", "|---|---|---|"]
    for pkg, url, lic in rows:
        out.append(f"| `{pkg}` | {lic or 'Unknown'} | {url or 'n/a'} |")
    out += ["", "### License Texts", ""]
    textless: list[str] = []
    for pkg, url, lic in rows:
        out += [f"#### {pkg}", "", f"* License: {lic or 'Unknown'}", f"* Source: {url or 'n/a'}", ""]
        pkg_dir = cache_dir / pkg
        files = sorted(p for p in pkg_dir.iterdir() if p.is_file()) if pkg_dir.exists() else []
        if not files:
            textless.append(pkg)
        for f in files:
            out += [f"##### {f.name}", "", "```text", f.read_text(errors="replace").rstrip(), "```", ""]
    if textless:
        sys.exit(
            "ERROR: go-licenses reported a license for these agent/go packages but saved no license text:\n"
            + "\n".join(f"  {t}" for t in textless)
        )
    return out, len(rows)


def agent_notices():
    py_section, py_count = _agent_python_notices()
    go_section, go_count = _agent_go_notices()

    out = [
        "# Third-Party Notices — Skyhook Agent",
        "",
        f"Agent tag: `{tag('agent')}`",
        "",
        "## Python Dependencies",
        "",
        *py_section,
        "## Go Dependencies",
        "",
        *go_section,
    ]
    AGENT_FILE.write_text(_collapse_blanks("\n".join(out)) + "\n")
    print(
        f"Wrote {AGENT_FILE.relative_to(REPO_ROOT)} "
        f"({py_count} Python deps, {go_count} Go deps)",
        file=sys.stderr,
    )


def demote_and_strip_h1(text: str) -> str:
    out, in_fence, dropped_h1 = [], False, False
    for line in text.splitlines():
        if line.startswith("```"):
            in_fence = not in_fence
            out.append(line)
            continue
        if not in_fence and not dropped_h1:
            if line.startswith("# "):
                dropped_h1 = True
                continue
            if not line.strip():
                continue
            dropped_h1 = True  # content before any H1 — keep what follows
        if not in_fence and line.startswith("#"):
            out.append("#" + line)
        else:
            out.append(line)
    return "\n".join(out)


def rollup():
    for f, hint in ((OPERATOR_FILE, "operator"), (AGENT_FILE, "agent")):
        if not f.exists():
            sys.exit(f"ERROR: {f.relative_to(REPO_ROOT)} does not exist. Run 'make notices-{hint}' first.")
    op = demote_and_strip_h1(OPERATOR_FILE.read_text())
    ag = demote_and_strip_h1(AGENT_FILE.read_text())
    out = [
        "# Third-Party Notices",
        "",
        "Combined third-party notices for Skyhook (operator, CLI, agent).",
        "",
        f"Operator tag: `{tag('operator')}`",
        f"Agent tag: `{tag('agent')}`",
        f"Chart tag: `{tag('chart')}`",
        "",
        "## Operator + CLI",
        "",
        op,
        "",
        "## Agent",
        "",
        ag,
        "",
    ]
    ROOT_FILE.write_text(_collapse_blanks("\n".join(out)))
    print(f"Wrote {ROOT_FILE.relative_to(REPO_ROOT)} (rollup)", file=sys.stderr)


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "all"
    if mode == "operator":
        operator_notices()
    elif mode == "agent":
        agent_notices()
    elif mode == "rollup":
        rollup()
    elif mode == "all":
        operator_notices()
        agent_notices()
        rollup()
    else:
        sys.exit("Usage: generate-notices.py [operator|agent|rollup|all]")


if __name__ == "__main__":
    main()
