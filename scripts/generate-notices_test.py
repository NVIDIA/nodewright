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


"""Tests for the license completeness gate in generate-notices.py.

A gate that only ever passes is indistinguishable from no gate, so every case
here that should be caught is asserted to be caught, not merely to not crash.
"""

import importlib.util
import tempfile
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "generate_notices", Path(__file__).with_name("generate-notices.py")
)
generate_notices = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(generate_notices)

uncovered_modules = generate_notices.uncovered_modules
license_files = generate_notices.license_files

LOCAL = "github.com/NVIDIA/nodewright/operator"


class UncoveredModulesTest(unittest.TestCase):
    def test_every_linked_module_covered(self):
        self.assertEqual(
            uncovered_modules(
                index_keys={"go.uber.org/zap", "golang.org/x/sys"},
                pkg_to_module={
                    "go.uber.org/zap": "go.uber.org/zap",
                    "golang.org/x/sys/unix": "golang.org/x/sys",
                },
                linked_modules={"go.uber.org/zap", "golang.org/x/sys"},
                local_module=LOCAL,
            ),
            [],
        )

    def test_missing_module_is_reported(self):
        self.assertEqual(
            uncovered_modules(
                index_keys={"go.uber.org/zap"},
                pkg_to_module={
                    "go.uber.org/zap": "go.uber.org/zap",
                    "golang.org/x/sys/unix": "golang.org/x/sys",
                },
                linked_modules={"go.uber.org/zap", "golang.org/x/sys"},
                local_module=LOCAL,
            ),
            ["golang.org/x/sys"],
        )

    def test_rollup_prefix_key_resolves_to_owning_module(self):
        # go-licenses keys a row on the directory a license covers, which is
        # often not a real import path.
        self.assertEqual(
            uncovered_modules(
                index_keys={"k8s.io/apimachinery/pkg"},
                pkg_to_module={"k8s.io/apimachinery/pkg/api/errors": "k8s.io/apimachinery"},
                linked_modules={"k8s.io/apimachinery"},
                local_module=LOCAL,
            ),
            [],
        )

    def test_bare_host_key_vouches_for_nothing(self):
        # go-licenses emits a bare "github.com" row when it cannot resolve a
        # package to a module. Under prefix matching that single row would
        # cover every module on the host and make this gate vacuous.
        self.assertEqual(
            uncovered_modules(
                index_keys={"github.com"},
                pkg_to_module={"github.com/spf13/cobra": "github.com/spf13/cobra"},
                linked_modules={"github.com/spf13/cobra"},
                local_module=LOCAL,
            ),
            ["github.com/spf13/cobra"],
        )

    def test_parent_module_does_not_vouch_for_nested_submodule(self):
        # A nested submodule is a separate module and may carry a different
        # license than its parent.
        self.assertEqual(
            uncovered_modules(
                index_keys={"go.opentelemetry.io/otel"},
                pkg_to_module={
                    "go.opentelemetry.io/otel": "go.opentelemetry.io/otel",
                    "go.opentelemetry.io/otel/metric": "go.opentelemetry.io/otel/metric",
                },
                linked_modules={"go.opentelemetry.io/otel", "go.opentelemetry.io/otel/metric"},
                local_module=LOCAL,
            ),
            ["go.opentelemetry.io/otel/metric"],
        )

    def test_local_module_and_its_submodules_are_not_required(self):
        self.assertEqual(
            uncovered_modules(
                index_keys=set(),
                pkg_to_module={f"{LOCAL}/internal/dal": LOCAL},
                linked_modules={LOCAL, f"{LOCAL}/tools", ""},
                local_module=LOCAL,
            ),
            [],
        )

    def test_missing_modules_are_sorted(self):
        self.assertEqual(
            uncovered_modules(
                index_keys=set(),
                pkg_to_module={"b.example/x": "b.example", "a.example/x": "a.example"},
                linked_modules={"b.example", "a.example"},
                local_module=LOCAL,
            ),
            ["a.example", "b.example"],
        )


class LicenseFilesTest(unittest.TestCase):
    """`go-licenses save --force` wipes its --save_path, so each platform saves
    into its own cache and the caches have to be merged on read."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def _write(self, platform, pkg, name, text):
        d = self.root / platform / pkg
        d.mkdir(parents=True, exist_ok=True)
        (d / name).write_text(text)

    def _caches(self, *platforms):
        return [self.root / p for p in platforms]

    def test_platform_exclusive_package_keeps_its_text(self):
        # golang.org/x/sys/unix is saved on the unix platforms and absent from
        # the windows cache. Reading only the last platform loses it entirely.
        self._write("linux_amd64", "golang.org/x/sys/unix", "LICENSE", "bsd text")
        found = license_files(
            "golang.org/x/sys/unix", self._caches("linux_amd64", "windows_arm64")
        )
        self.assertEqual([f.read_text() for f in found], ["bsd text"])

    def test_last_platform_alone_would_lose_it(self):
        self._write("linux_amd64", "golang.org/x/sys/unix", "LICENSE", "bsd text")
        self.assertEqual(license_files("golang.org/x/sys/unix", self._caches("windows_arm64")), [])

    def test_duplicate_across_platforms_is_not_repeated(self):
        self._write("linux_amd64", "example.com/pkg", "LICENSE", "same text")
        self._write("windows_arm64", "example.com/pkg", "LICENSE", "same text")
        found = license_files("example.com/pkg", self._caches("linux_amd64", "windows_arm64"))
        self.assertEqual(len(found), 1)

    def test_distinct_file_names_are_unioned_and_sorted(self):
        self._write("linux_amd64", "example.com/pkg", "LICENSE", "a")
        self._write("windows_arm64", "example.com/pkg", "NOTICE", "b")
        found = license_files("example.com/pkg", self._caches("linux_amd64", "windows_arm64"))
        self.assertEqual([f.name for f in found], ["LICENSE", "NOTICE"])

    def test_unknown_package_yields_nothing(self):
        self._write("linux_amd64", "example.com/pkg", "LICENSE", "a")
        self.assertEqual(license_files("example.com/other", self._caches("linux_amd64")), [])


if __name__ == "__main__":
    unittest.main()
