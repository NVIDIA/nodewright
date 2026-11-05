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

"""Builds the architecture diagram SVGs. Run via generate.sh, which renders the
SVGs this emits into the committed PNGs one directory up.

Geometry is hand-tuned. Two things are easy to reintroduce when editing:
  * a dashed path can end on a gap in its dash pattern, which detaches the
    arrowhead — use curve(..., marker=False) plus stub() for the tip;
  * marker orient must be "auto" (auto-start-reverse only applies to
    marker-start, and misorients vertical arrows here).
Always eyeball the rendered PNGs; overlaps are invisible in the source.
"""

import argparse
import pathlib

from theme import *  # noqa: F403  - the theme is a deliberate namespace of drawing primitives

OUT = pathlib.Path(__file__).parent


# ───────────────────────── 1. CR lifecycle ─────────────────────────
def cr_lifecycle():
    W, H = 1280, 520
    p = [head(W, H), title(48, 58, "Lifecycle of a NodeWright in a cluster")]
    p.append(sub(48, 82, "cluster-scoped resource → per-node work → convergence", 14))

    labels = [
        ("apply CR", "kubectl apply"),
        ("admission", "webhook validates"),
        ("reconcile", "observe cluster"),
        ("select nodes", "selector → budget"),
        ("per-node stages", "cordon → DAG → uncordon"),
        ("complete", "status: complete"),
    ]
    bw, bh, gap, x0, y = 172, 76, 30, 48, 140
    for i, (lab, note) in enumerate(labels):
        x = x0 + i * (bw + gap)
        kind = "solid" if lab in ("per-node stages",) else "neutral"
        p.append(box(x, y, bw, bh, lab, kind, note, fs=15))
        if i:
            p.append(arrow(x - gap + 3, y + bh / 2, x - 2, y + bh / 2))

    # requeue loop: complete -> reconcile
    lastx = x0 + 5 * (bw + gap)
    rx = x0 + 2 * (bw + gap) + bw / 2
    p.append(curve(f"M{lastx + bw/2},{y + bh + 6} C{lastx + bw/2},{y + bh + 70} "
                   f"{rx},{y + bh + 70} {rx},{y + bh + 26}", muted=True, dash=True, marker=False,
                   label="spec or config change → reconcile again",
                   lx=(lastx + rx) / 2 + 40, ly=y + bh + 88))
    p.append(stub(rx, y + bh + 26, rx, y + bh + 2))

    # deletion band
    dy = 330
    p.append(f'<line x1="48" y1="{dy - 16}" x2="{W - 48}" y2="{dy - 16}" stroke="{STROKE}" stroke-width="1"/>')
    p.append(sub(48, dy + 8, "DELETION", 12, GREEN_LIT))
    dlab = [
        ("delete CR", "kubectl delete"),
        ("finalizer holds", "nodewright.nvidia.com/nodewright"),
        ("uninstall runs", "where uninstall.enabled"),
        ("metadata cleaned", "node annotations + taints"),
        ("removed", "finalizer released"),
    ]
    bw2, gap2, y2 = 208, 26, dy + 28
    for i, (lab, note) in enumerate(dlab):
        x = 48 + i * (bw2 + gap2)
        p.append(box(x, y2, bw2, bh, lab, "neutral", note, fs=15))
        if i:
            p.append(arrow(x - gap2 + 3, y2 + bh / 2, x - 2, y2 + bh / 2))

    p.append(sub(48, H - 26, "State for every node lives in annotations on the Node "
                             "(nodewright.nvidia.com/nodeState_&lt;name&gt;), not on the CR.", 13))
    p.append("</svg>")
    (OUT / "cr-lifecycle.svg").write_text("\n".join(p))


# ───────────────────────── 2. package stages ─────────────────────────
def package_stages():
    W, H = 1288, 690
    bw, bh = 214, 66
    p = [head(W, H), title(48, 58, "Package stages on one node")]
    p.append(sub(48, 82, "install and uninstall are separate lanes; interrupt stages appear "
                         "only when the package declares an interrupt", 14))

    p.append(sub(96, 112, "INSTALL AND UPDATE", 12, GREEN_LIT))
    ap   = (96, 128)
    up   = (96, 234)
    cfg  = (378, 181)
    itr  = (660, 116)
    post = (660, 214)
    done = (942, 181)
    p.append(box(*ap, bw, bh, "apply", "solid", "first install", fs=16))
    p.append(box(*up, bw, bh, "upgrade", "solid", "version increased", fs=16))
    p.append(box(*cfg, bw, bh, "config", "solid", "configMap delivered", fs=16))
    p.append(box(*itr, bw, bh, "interrupt", "solid", "reboot / restart services", fs=16))
    p.append(box(*post, bw, bh, "post-interrupt", "solid", "settle after interrupt", fs=16))
    p.append(box(*done, bw, bh, "complete", "neutral", "state recorded on Node", fs=16))

    p.append(curve(f"M{ap[0]+bw+4},{ap[1]+bh/2} C{ap[0]+bw+70},{ap[1]+bh/2} "
                   f"{cfg[0]-60},{cfg[1]+22} {cfg[0]-2},{cfg[1]+22}"))
    p.append(curve(f"M{up[0]+bw+4},{up[1]+bh/2} C{up[0]+bw+70},{up[1]+bh/2} "
                   f"{cfg[0]-60},{cfg[1]+bh-16} {cfg[0]-2},{cfg[1]+bh-16}"))
    p.append(curve(f"M{cfg[0]+bw+4},{cfg[1]+16} C{cfg[0]+bw+70},{cfg[1]+16} "
                   f"{itr[0]-60},{itr[1]+bh/2} {itr[0]-2},{itr[1]+bh/2}",
                   label="interrupt declared", lx=(cfg[0]+bw+itr[0])/2+6, ly=itr[1]-8))
    p.append(curve(f"M{cfg[0]+bw/2},{cfg[1]+bh+4} C{cfg[0]+bw/2},{post[1]+bh+54} "
                   f"{done[0]+bw/2},{post[1]+bh+54} {done[0]+bw/2},{done[1]+bh+26}",
                   muted=True, dash=True, marker=False, label="no interrupt",
                   lx=(cfg[0]+done[0])/2+bw/2, ly=post[1]+bh+76))
    p.append(stub(done[0]+bw/2, done[1]+bh+26, done[0]+bw/2, done[1]+bh+2))
    p.append(arrow(itr[0]+bw/2, itr[1]+bh+4, post[0]+bw/2, post[1]-6))
    p.append(curve(f"M{post[0]+bw+4},{post[1]+bh/2} C{post[0]+bw+60},{post[1]+bh/2} "
                   f"{done[0]-60},{done[1]+bh/2} {done[0]-2},{done[1]+bh/2}"))

    divy = 384
    p.append(f'<line x1="48" y1="{divy}" x2="{W-48}" y2="{divy}" stroke="{STROKE}" stroke-width="1"/>')
    p.append(sub(96, divy + 26, "UNINSTALL", 12, GREEN_LIT))
    un  = (96, divy + 42)
    uni = (378, divy + 42)
    rem = (660, divy + 42)
    p.append(box(*un, bw, bh, "uninstall", "solid", "uninstall.apply: true", fs=16))
    p.append(box(*uni, bw, bh, "uninstall-interrupt", "solid", "only if interrupt declared", fs=14))
    p.append(box(*rem, bw, bh, "state removed", "neutral", "absent = uninstalled", fs=15))
    p.append(arrow(un[0]+bw+4, un[1]+bh/2, uni[0]-6, uni[1]+bh/2))
    p.append(arrow(uni[0]+bw+4, uni[1]+bh/2, rem[0]-6, rem[1]+bh/2))
    p.append(curve(f"M{un[0]+bw/2},{un[1]+bh+4} C{un[0]+bw/2},{un[1]+bh+50} "
                   f"{rem[0]+bw/2},{un[1]+bh+50} {rem[0]+bw/2},{rem[1]+bh+26}",
                   muted=True, dash=True, marker=False, label="no interrupt declared",
                   lx=(un[0]+rem[0])/2+bw/2, ly=un[1]+bh+68))
    p.append(stub(rem[0]+bw/2, rem[1]+bh+26, rem[0]+bw/2, rem[1]+bh+2))

    p.append(curve(f"M{un[0]-4},{un[1]+bh/2} L60,{un[1]+bh/2} L60,{ap[1]+bh/2} "
                   f"L{ap[0]-22},{ap[1]+bh/2}", muted=True, dash=True, marker=False))
    p.append(stub(ap[0]-22, ap[1]+bh/2, ap[0]-2, ap[1]+bh/2))
    p.append(sub(ap[0], ap[1]+bh+24, "cancel — uninstall.apply → false", 12, MUTE))

    p.append(sub(48, H - 82, "An explicit uninstall ends by removing the package's state. "
                             "It does not re-apply while uninstall.apply is true.", 13))
    p.append(sub(48, H - 56, "Every stage above runs its work step and then its paired check step.", 15, TEXT))
    p.append(sub(48, H - 30, "interrupt is the one exception — it has no check step.", 15, GREEN_LIT))
    p.append("</svg>")
    (OUT / "package-stages.svg").write_text(chr(10).join(p))


# ───────────────────────── 3. hero: stage pod anatomy ─────────────────────────
def stage_pod():
    W, H = 1280, 660
    p = [head(W, H), title(48, 58, "Inside one stage: the work step and its check step")]
    p.append(sub(48, 82, "each stage is one pod; the steps are initContainers, so Kubernetes "
                         "runs them in order and stops at the first failure", 14))

    # left: stage pod
    px, py, pw, ph = 48, 116, 620, 436
    p.append(f'<rect x="{px}" y="{py}" width="{pw}" height="{ph}" rx="10" fill="{PANEL}" '
             f'stroke="{STROKE}" stroke-width="1.5"/>')
    p.append(sub(px + 20, py + 30, "STAGE POD", 12, GREEN_LIT))
    p.append(sub(px + 20, py + 50, "one per package, per stage, per node", 12))

    bw, bh, bx = 452, 62, px + 84
    steps = [
        ("&lt;package&gt;-init", "copies the package onto the host", "neutral"),
        ("&lt;package&gt;-&lt;stage&gt;", "agent runs the work step", "solid"),
        ("&lt;package&gt;-&lt;stage&gt;check", "agent runs &lt;stage&gt;-check", "outline"),
    ]
    y = py + 72
    for i, (lab, note, kind) in enumerate(steps):
        p.append(box(bx, y, bw, bh, lab, kind, note, fs=15))
        p.append(sub(px + 30, y + bh / 2 + 5, f"{i+1}", 15, GREEN_LIT))
        if i:
            p.append(arrow(bx + bw / 2, y - 26, bx + bw / 2, y - 3))
        y += bh + 26
    p.append(f'<line x1="{bx}" y1="{y - 12}" x2="{bx + bw}" y2="{y - 12}" '
             f'stroke="{STROKE}" stroke-width="1" stroke-dasharray="4 4"/>')
    p.append(box(bx, y, bw, 46, "pause", "dashed", None, fs=14))
    p.append(sub(px + 30, y + 28, "4", 15, MUTE))
    p.append(sub(px + 20, py + ph - 18, "initContainers 1→3 run in order · "
                                        "a failure halts the pod and the stage retries", 12, MUTE))

    # right: interrupt pod (the exception) — dashed box wraps ONLY the pod
    qx, qw, qh = 700, 532, 290
    p.append(f'<rect x="{qx}" y="{py}" width="{qw}" height="{qh}" rx="10" fill="{PANEL}" '
             f'stroke="{DASH}" stroke-width="1.5" stroke-dasharray="7 5"/>')
    p.append(sub(qx + 20, py + 30, "INTERRUPT POD — THE EXCEPTION", 12, GREEN_LIT))
    p.append(sub(qx + 20, py + 50, "built separately; no check container exists", 12))
    iy = py + 72
    p.append(box(qx + 40, iy, 452, bh, "&lt;package&gt;-init", "neutral",
                 "creates the package's copy directory", fs=15))
    p.append(sub(qx + 22, iy + bh / 2 + 5, "1", 15, GREEN_LIT))
    iy += bh + 26
    p.append(arrow(qx + 40 + 226, iy - 26, qx + 40 + 226, iy - 3))
    p.append(box(qx + 40, iy, 452, bh, "interrupt", "solid", "agent runs the interrupt", fs=15))
    p.append(sub(qx + 22, iy + bh / 2 + 5, "2", 15, GREEN_LIT))
    iy += bh + 16
    p.append(box(qx + 40, iy, 452, 40, "pause", "dashed", None, fs=14))
    p.append(sub(qx + 22, iy + 25, "3", 15, MUTE))

    # pairing list — outside the interrupt pod box, it describes every stage
    ly = py + qh + 52
    p.append(sub(qx + 24, ly, "WORK STEP → CHECK STEP", 12, GREEN_LIT))
    pairs = [("uninstall", "uninstall-check"), ("upgrade", "upgrade-check"),
             ("apply", "apply-check"), ("config", "config-check"),
             ("post-interrupt", "post-interrupt-check"), ("interrupt", "—  none")]
    for i, (a, b) in enumerate(pairs):
        yy = ly + 26 + i * 22
        last = i == len(pairs) - 1
        p.append(sub(qx + 24, yy, a, 13, MUTE if last else TEXT, mono=True))
        p.append(sub(qx + 190, yy, "→", 13, DASH if last else GREEN))
        p.append(sub(qx + 220, yy, b, 13, MUTE if last else GREEN_LIT, mono=True))

    p.append(sub(48, H - 26, "A stage counts as done only when its check step passes. "
                             "Any failing check fails the whole stage.", 14, TEXT))
    p.append("</svg>")
    (OUT / "stage-pod-anatomy.svg").write_text("\n".join(p))



# ───────────────────────── 4. job → pod → stage → scripts ─────────────────────────
def job_nesting():
    W, H = 1400, 712
    p = [head(W, H), title(48, 58, "Job → Pod → stage containers → scripts")]
    p.append(sub(48, 82, "what the operator creates for one package stage on one node, "
                         "and what the scripts end up running against", 14))

    # ---- nested Job > Pod > containers ----
    jx, jy, jw, jh = 48, 128, 700, 352
    p.append(f'<rect x="{jx}" y="{jy}" width="{jw}" height="{jh}" rx="10" fill="{PANEL}" '
             f'stroke="{GREEN_DIM}" stroke-width="2"/>')
    p.append(sub(jx + 20, jy + 28, "JOB — batch/v1", 12, GREEN_LIT))
    p.append(sub(jx + 20, jy + 48, "parallelism 1 · completions 1 · backoffLimit · owns the retry budget", 12))

    px, py, pw, ph = jx + 28, jy + 70, jw - 56, jh - 92
    p.append(f'<rect x="{px}" y="{py}" width="{pw}" height="{ph}" rx="8" fill="{BG}" '
             f'stroke="{STROKE}" stroke-width="1.5"/>')
    p.append(sub(px + 18, py + 26, "POD — pinned to one node", 12, GREEN_LIT))

    rows = [("1  &lt;package&gt;-init", "copies /skyhook-package/* onto the host", "neutral"),
            ("2  &lt;package&gt;-&lt;stage&gt;", "WORK   agent runs &lt;stage&gt;", "solid"),
            ("3  &lt;package&gt;-&lt;stage&gt;check", "CHECK  agent runs &lt;stage&gt;-check", "outline"),
            ("4  done", "exits 0 so the Job can succeed", "dashed")]
    ry, rh = py + 44, 44
    for i, (lab, note, kind) in enumerate(rows):
        yy = ry + i * (rh + 10)
        if kind == "solid":
            fill, stroke, tc, da = GREEN, GREEN, ON_GREEN, ""
        elif kind == "outline":
            fill, stroke, tc, da = BG, GREEN, GREEN_LIT, ""
        elif kind == "dashed":
            fill, stroke, tc, da = BG, DASH, MUTE, ' stroke-dasharray="6 5"'
        else:
            fill, stroke, tc, da = PANEL, STROKE, TEXT, ""
        p.append(f'<rect x="{px+18}" y="{yy}" width="{pw-36}" height="{rh}" rx="6" fill="{fill}" '
                 f'stroke="{stroke}" stroke-width="1.5"{da}/>')
        p.append(f'<text x="{px+34}" y="{yy+27}" font-family="{MONO}" font-size="14" '
                 f'font-weight="600" fill="{tc}">{lab}</text>')
        nc = ON_GREEN if kind == "solid" else MUTE
        p.append(f'<text x="{px+pw-54}" y="{yy+27}" font-family="{FONT}" font-size="12" '
                 f'fill="{nc}" text-anchor="end" opacity="0.9">{note}</text>')
        if i:
            p.append(stub(px + pw/2, yy - 12, px + pw/2, yy - 2, muted=False))

    # ---- side-loaded volumes ----
    mx, mw = 796, 556
    p.append(sub(mx, jy + 28, "SIDE-LOADED INTO THE POD", 12, GREEN_LIT))
    mounts = [
        ("hostPath  /", "→ /root   (HostToContainer propagation)",
         "the node's real filesystem"),
        ("ConfigMap  &lt;nw&gt;-&lt;node&gt;-metadata", "→ /skyhook-package/node-metadata",
         "annotations.json · labels.json · packages.json"),
        ("ConfigMap  &lt;nw&gt;-&lt;pkg&gt;-&lt;ver&gt;", "→ /skyhook-package/configmaps/&lt;key&gt;",
         "one subPath mount per key — overlays, never replaces"),
    ]
    my = jy + 46
    for lab, path, note in mounts:
        p.append(f'<rect x="{mx}" y="{my}" width="{mw}" height="92" rx="8" fill="{PANEL}" '
                 f'stroke="{STROKE}" stroke-width="1.5"/>')
        p.append(f'<text x="{mx+20}" y="{my+30}" font-family="{MONO}" font-size="14" '
                 f'font-weight="600" fill="{TEXT}">{lab}</text>')
        p.append(f'<text x="{mx+20}" y="{my+54}" font-family="{MONO}" font-size="13" '
                 f'fill="{GREEN_LIT}">{path}</text>')
        p.append(sub(mx + 20, my + 76, note, 12))
        my += 104

    # ---- bottom band: copy then chroot ----
    divy = 502
    p.append(f'<line x1="48" y1="{divy}" x2="{W-48}" y2="{divy}" stroke="{STROKE}" stroke-width="1"/>')
    p.append(sub(48, divy + 26, "WHAT THE SCRIPTS RUN AGAINST", 12, GREEN_LIT))
    bw2, by = 380, divy + 42
    steps = [("/skyhook-package/*", "image content + both configMaps", "neutral"),
             ("/var/lib/skyhook/&lt;nw&gt;/&lt;pkg&gt;-&lt;ver&gt;", "copied onto the host by init", "solid"),
             ("skyhook_dir/*.sh", "agent chroots to /root and runs", "outline")]
    for i, (lab, note, kind) in enumerate(steps):
        x = 48 + i * (bw2 + 40)
        p.append(box(x, by, bw2, 72, lab, kind, note, fs=14))
        if i:
            p.append(stub(x - 40 + 4, by + 36, x - 2, by + 36, muted=False))

    p.append(sub(48, H - 28, "The scripts never read the ConfigMaps through the API — by the time they run, "
                             "both are ordinary files on the host under the copy directory.", 13))
    p.append("</svg>")
    (OUT / "job-nesting.svg").write_text(chr(10).join(p))

def main():
    global OUT

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", default=None,
                        help="directory to write the intermediate SVGs into "
                             "(defaults to this script's directory)")
    args = parser.parse_args()

    if args.out:
        OUT = pathlib.Path(args.out)
    OUT.mkdir(parents=True, exist_ok=True)

    cr_lifecycle()
    package_stages()
    stage_pod()
    job_nesting()
    print("wrote:", ", ".join(sorted(f.name for f in OUT.glob("*.svg"))))


if __name__ == "__main__":
    main()
