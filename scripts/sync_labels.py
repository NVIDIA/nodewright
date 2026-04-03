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


import subprocess
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    yaml = None

REPO_ROOT = Path(__file__).resolve().parent.parent
LABELS_FILE = REPO_ROOT / ".github" / "labels.yml"


def load_labels():
    with open(LABELS_FILE) as f:
        content = f.read()

    if yaml:
        return yaml.safe_load(content)

    # Minimal parser: handles simple list of {name, color, description} mappings
    labels = []
    current = {}
    for line in content.splitlines():
        line = line.rstrip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("- name:"):
            if current:
                labels.append(current)
            current = {"name": line.split(":", 1)[1].strip().strip('"')}
        elif line.strip().startswith("color:"):
            current["color"] = line.split(":", 1)[1].strip().strip('"')
        elif line.strip().startswith("description:"):
            current["description"] = line.split(":", 1)[1].strip().strip('"')
    if current:
        labels.append(current)
    return labels


def get_repo():
    result = subprocess.run(
        ["gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
        capture_output=True, text=True
    )
    if result.returncode != 0:
        print("ERROR: Could not determine repo. Are you logged in with gh?")
        sys.exit(1)
    return result.stdout.strip()


def sync_label(repo, label):
    name = label["name"]
    color = label.get("color", "ededed")
    description = label.get("description", "")

    result = subprocess.run(
        ["gh", "label", "create", name,
         "--color", color,
         "--description", description,
         "--repo", repo,
         "--force"],
        capture_output=True, text=True
    )
    if result.returncode == 0:
        print(f"  ✓ {name}")
    else:
        print(f"  ✗ {name}: {result.stderr.strip()}")
        return False
    return True


def main():
    labels = load_labels()
    repo = get_repo()
    print(f"Syncing {len(labels)} labels to {repo}...")

    ok = sum(sync_label(repo, l) for l in labels)
    failed = len(labels) - ok
    print(f"\nDone: {ok} synced, {failed} failed.")
    if failed:
        sys.exit(1)


if __name__ == "__main__":
    main()
