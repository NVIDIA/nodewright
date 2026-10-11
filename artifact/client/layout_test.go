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

package client

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"oras.land/oras-go/v2/content/oci"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
)

// layoutPayload is a payload tree with an executable, a private file, a
// directory of its own mode, and relative and absolute symlinks.
var layoutPayload = []extractEntry{
	extractFile("config.json", 0o644, inspectConfig),
	extractDir("bin", 0o750),
	extractFile("bin/run.sh", 0o755, "#!/bin/sh\necho run\n"),
	extractFile("bin/secret", 0o600, "secret"),
	extractLink("run", "bin/run.sh"),
	extractLink("hostname", "/etc/hostname"),
}

// layoutSeed is an existing destination that differs from layoutPayload in
// contents, modes and a symlink.
var layoutSeed = []extractEntry{
	extractFile("config.json", 0o600, `{"old":true}`),
	extractDir("bin", 0o700),
	extractFile("bin/run.sh", 0o700, "old"),
	extractFile("run", 0o644, "not a symlink"),
	extractLink("hostname", "/etc/old"),
	extractLink("stale", "bin/run.sh"),
}

// layoutWrite writes entries, in order, beneath dir, creating dir. Directory
// modes are set last, children first, so a read-only directory still
// receives its entries.
func layoutWrite(dir string, entries ...extractEntry) {
	GinkgoHelper()
	DeferCleanup(layoutWritable, dir)
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	dirModes := map[string]fs.FileMode{}
	for _, e := range entries {
		path := filepath.Join(dir, e.hdr.Name)
		switch e.hdr.Typeflag {
		case tar.TypeDir:
			Expect(os.Mkdir(path, 0o755)).To(Succeed())
			dirModes[path] = fs.FileMode(e.hdr.Mode)
		case tar.TypeSymlink:
			Expect(os.Symlink(e.hdr.Linkname, path)).To(Succeed())
		default:
			Expect(os.WriteFile(path, []byte(e.body), 0o600)).To(Succeed())
			Expect(os.Chmod(path, fs.FileMode(e.hdr.Mode))).To(Succeed())
		}
	}
	for _, path := range slices.Backward(slices.Sorted(maps.Keys(dirModes))) {
		Expect(os.Chmod(path, dirModes[path])).To(Succeed())
	}
}

// layoutSource writes entries into a new temp directory and returns it.
func layoutSource(entries ...extractEntry) string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	layoutWrite(dir, entries...)
	return dir
}

// layoutWritable gives every directory beneath dir mode 0o755, so that a
// temp directory holding a read-only one can be removed.
func layoutWritable(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			err = os.Chmod(path, 0o755)
		}
		return err
	})
}

// layoutNames lists the names in dir.
func layoutNames(dir string) []string {
	GinkgoHelper()
	entries, err := os.ReadDir(dir)
	Expect(err).NotTo(HaveOccurred())
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// layoutCancelTransport cancels a context once a GET of path has been sent
// and its response headers received, so the registry has logged it.
type layoutCancelTransport struct {
	base   http.RoundTripper
	path   string
	cancel context.CancelFunc
}

func (t layoutCancelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if req.Method == http.MethodGet && req.URL.Path == t.path {
		t.cancel()
	}
	return resp, err
}

var _ = Describe("Layout", func() {
	var (
		ctx context.Context
		reg *registrytest.Registry
		// base holds only rootDir, the directory root opens, and outside
		// when a spec creates it.
		base    string
		rootDir string
		root    *os.Root
	)

	BeforeEach(func() {
		ctx = context.Background()
		reg = registrytest.New(registrytest.Options{})
		DeferCleanup(reg.Close)
		base = GinkgoT().TempDir()
		DeferCleanup(layoutWritable, base)
		rootDir = filepath.Join(base, "root")
		Expect(os.Mkdir(rootDir, 0o755)).To(Succeed())
		var err error
		root, err = os.OpenRoot(rootDir)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(root.Close)
	})

	// layoutSpec is a package of one payload layer per directory, whose
	// metadata config is the config.json of the first.
	layoutSpec := func(dirs ...string) registrytest.PackageSpec {
		spec := registrytest.PackageSpec{Uninstall: artifact.UninstallSupported}
		for _, dir := range dirs {
			spec.Payload = append(spec.Payload, registrytest.Layer{Dir: dir})
		}
		return spec
	}

	// layoutInspect pushes spec to inspectRepo, tagged tag, and inspects it.
	layoutInspect := func(tag string, spec registrytest.PackageSpec) *Inspection {
		GinkgoHelper()
		_, _, err := registrytest.PushPackage(ctx, reg.Repository(inspectRepo), tag, spec)
		Expect(err).NotTo(HaveOccurred())
		in, err := Inspect(ctx, inspectRemote(reg, Credential{}, tag, ""))
		Expect(err).NotTo(HaveOccurred())
		return in
	}

	// layoutPackage inspects a package of layoutPayload alone.
	layoutPackage := func() *Inspection {
		GinkgoHelper()
		return layoutInspect("1.0", layoutSpec(layoutSource(layoutPayload...)))
	}

	It("reproduces the payload tree, with its modes and symlinks, in a destination of mode 0o755 and nothing beside it", func() {
		src := layoutSource(layoutPayload...)
		Expect(Layout(ctx, layoutInspect("1.0", layoutSpec(src)), root, "pkg")).To(Succeed())

		want := extractTree(src)
		Expect(want).To(HaveKeyWithValue("bin/run.sh", "-rwxr-xr-x #!/bin/sh\necho run\n"))
		Expect(want).To(HaveKeyWithValue("hostname", "-> /etc/hostname"))
		Expect(extractTree(filepath.Join(rootDir, "pkg"))).To(Equal(want))
		info, err := os.Lstat(filepath.Join(rootDir, "pkg"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode()).To(Equal(fs.ModeDir | 0o755))
		Expect(layoutNames(rootDir)).To(ConsistOf("pkg"))
	})

	It("applies two payload layers in manifest order", func() {
		first := layoutSource(
			extractFile("config.json", 0o644, inspectConfig),
			extractFile("both", 0o644, "first"),
			extractFile("first", 0o644, "first"),
		)
		second := layoutSource(extractFile("both", 0o755, "second"), extractFile("second", 0o644, "second"))
		in := layoutInspect("1.0", layoutSpec(first, second))
		Expect(in.Package.Payload).To(HaveLen(2))

		Expect(Layout(ctx, in, root, "pkg")).To(Succeed())
		Expect(extractTree(filepath.Join(rootDir, "pkg"))).To(Equal(map[string]string{
			"config.json": "-rw-r--r-- " + inspectConfig,
			"both":        "-rwxr-xr-x second",
			"first":       "-rw-r--r-- first",
			"second":      "-rw-r--r-- second",
		}))
	})

	It("lets a later payload layer add, overwrite and replace entries in a directory of mode 0o555 an earlier layer laid down", func() {
		spec := registrytest.PackageSpec{
			Uninstall: artifact.UninstallSupported,
			Config:    json.RawMessage(inspectConfig),
			Payload: []registrytest.Layer{
				{Gzip: extractGzip(
					extractFile("config.json", 0o644, inspectConfig),
					extractDir("ro", 0o555),
					extractFile("ro/old", 0o644, "one"),
					extractDir("ro/sub", 0o555),
					extractFile("ro/sub/x", 0o644, "one"),
					extractDir("ro/keep", 0o555),
				)},
				{Gzip: extractGzip(
					extractFile("ro/new", 0o644, "two"),
					extractFile("ro/old", 0o600, "two"),
					extractFile("ro/sub", 0o644, "two"),
					extractDir("ro/keep", 0o750),
					extractFile("ro/keep/y", 0o644, "two"),
				)},
			},
		}

		Expect(Layout(ctx, layoutInspect("1.0", spec), root, "pkg")).To(Succeed())
		Expect(extractTree(filepath.Join(rootDir, "pkg"))).To(Equal(map[string]string{
			"config.json": "-rw-r--r-- " + inspectConfig,
			"ro":          "dr-xr-xr-x",
			"ro/new":      "-rw-r--r-- two",
			"ro/old":      "-rw------- two",
			"ro/sub":      "-rw-r--r-- two",
			"ro/keep":     "drwxr-x---",
			"ro/keep/y":   "-rw-r--r-- two",
		}))
	})

	It("creates a destination several levels deep with its parents", func() {
		src := layoutSource(layoutPayload...)
		Expect(Layout(ctx, layoutInspect("1.0", layoutSpec(src)), root, filepath.Join("a", "b", "c", "pkg"))).To(Succeed())

		Expect(layoutNames(filepath.Join(rootDir, "a"))).To(ConsistOf("b"))
		Expect(layoutNames(filepath.Join(rootDir, "a", "b"))).To(ConsistOf("c"))
		Expect(layoutNames(filepath.Join(rootDir, "a", "b", "c"))).To(ConsistOf("pkg"))
		Expect(extractTree(filepath.Join(rootDir, "a", "b", "c", "pkg"))).To(Equal(extractTree(src)))
	})

	It("follows a relative symlink in the parent path that stays inside the root", func() {
		Expect(os.MkdirAll(filepath.Join(rootDir, "real", "sub"), 0o755)).To(Succeed())
		Expect(os.Symlink(filepath.Join("real", "sub"), filepath.Join(rootDir, "link"))).To(Succeed())
		src := layoutSource(layoutPayload...)

		Expect(Layout(ctx, layoutInspect("1.0", layoutSpec(src)), root, filepath.Join("link", "pkg"))).To(Succeed())
		Expect(layoutNames(rootDir)).To(ConsistOf("link", "real"))
		Expect(layoutNames(filepath.Join(rootDir, "real", "sub"))).To(ConsistOf("pkg"))
		Expect(extractTree(filepath.Join(rootDir, "real", "sub", "pkg"))).To(Equal(extractTree(src)))
	})

	DescribeTable("fails a symlink in the parent path that is absolute or leads out of the root, writing nothing outside it and leaving no staging",
		func(linkTarget func(outside string) string) {
			in := layoutPackage()
			outside := filepath.Join(base, "outside")
			Expect(os.Mkdir(outside, 0o755)).To(Succeed())
			Expect(os.Symlink(linkTarget(outside), filepath.Join(rootDir, "link"))).To(Succeed())
			before := extractTree(rootDir)

			Expect(Layout(ctx, in, root, filepath.Join("link", "pkg"))).NotTo(Succeed())
			Expect(extractTree(outside)).To(BeEmpty())
			Expect(layoutNames(base)).To(ConsistOf("root", "outside"))
			Expect(extractTree(rootDir)).To(Equal(before))
		},
		Entry("an absolute target", func(outside string) string { return outside }),
		Entry("a relative target that leads out", func(string) string { return filepath.Join("..", "outside") }),
	)

	DescribeTable("fails without changing an existing destination or leaving anything beside it",
		func(run func() error, check func(error)) {
			dest := filepath.Join(rootDir, "pkg")
			layoutWrite(dest, layoutSeed...)
			Expect(os.Chmod(dest, 0o750)).To(Succeed())
			before := extractTree(rootDir)
			Expect(before).To(HaveKeyWithValue("pkg/stale", "-> bin/run.sh"))

			check(run())
			Expect(extractTree(rootDir)).To(Equal(before))
		},
		Entry("on payload bytes tampered at the same length",
			func() error {
				src := layoutSource(layoutPayload...)
				in := layoutInspect("1.0", layoutSpec(src))
				gz, err := registrytest.TarGzip(src)
				Expect(err).NotTo(HaveOccurred())
				// The trailer holds the gzip checksum, so every entry is laid
				// out before the blob fails to verify.
				gz[len(gz)-1] ^= 0xff
				reg.Tamper(in.Package.Payload[0].Digest, gz)
				return Layout(ctx, in, root, "pkg")
			},
			func(err error) { expectSentinel(err, ErrDigestMismatch) }),
		Entry("on payload bytes tampered at a different length",
			func() error {
				src := layoutSource(layoutPayload...)
				in := layoutInspect("1.0", layoutSpec(src))
				gz, err := registrytest.TarGzip(src)
				Expect(err).NotTo(HaveOccurred())
				reg.Tamper(in.Package.Payload[0].Digest, append(gz, 0))
				return Layout(ctx, in, root, "pkg")
			},
			func(err error) { expectSentinel(err, ErrDigestMismatch) }),
		Entry("on a rejected entry",
			func() error {
				spec := registrytest.PackageSpec{
					Uninstall: artifact.UninstallSupported,
					Config:    json.RawMessage(inspectConfig),
					Payload: []registrytest.Layer{{Gzip: extractGzip(
						extractFile("config.json", 0o644, inspectConfig),
						extractFile("run", 0o755, "run"),
						extractEntry{hdr: tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "run"}},
					)}},
				}
				return Layout(ctx, layoutInspect("1.0", spec), root, "pkg")
			},
			func(err error) { extractExpectReason(err, artifact.ReasonLayerEntry) }),
		Entry("on a config.json that differs from the metadata config",
			func() error {
				spec := layoutSpec(layoutSource(layoutPayload...))
				spec.Config = json.RawMessage(`{"schema_version":"v1","package_name":"other","package_version":"1.0.0","modes":{}}`)
				return Layout(ctx, layoutInspect("1.0", spec), root, "pkg")
			},
			func(err error) { extractExpectReason(err, artifact.ReasonConfigMismatch) }),
		Entry("on a missing config.json",
			func() error {
				spec := layoutSpec(layoutSource(layoutPayload[1:]...))
				spec.Config = json.RawMessage(inspectConfig)
				return Layout(ctx, layoutInspect("1.0", spec), root, "pkg")
			},
			func(err error) { extractExpectReason(err, artifact.ReasonConfigMismatch) }),
		Entry("on a config.json that is a symlink to an equal file",
			func() error {
				spec := layoutSpec(layoutSource(
					extractFile("real.json", 0o644, inspectConfig),
					extractLink("config.json", "real.json"),
				))
				spec.Config = json.RawMessage(inspectConfig)
				return Layout(ctx, layoutInspect("1.0", spec), root, "pkg")
			},
			func(err error) { extractExpectReason(err, artifact.ReasonConfigMismatch) }),
		Entry("on a config mismatch after a payload with a directory of mode 0o555",
			func() error {
				spec := layoutSpec(layoutSource(slices.Concat(layoutPayload, []extractEntry{
					extractDir("ro", 0o555),
					extractFile("ro/child", 0o644, "child"),
				})...))
				spec.Config = json.RawMessage(`{}`)
				return Layout(ctx, layoutInspect("1.0", spec), root, "pkg")
			},
			func(err error) { extractExpectReason(err, artifact.ReasonConfigMismatch) }),
		Entry("on a context cancelled once the payload blob GET is sent",
			func() error {
				ref, err := NewReference(reg.Host()+"/"+inspectRepo, "1.0", "")
				Expect(err).NotTo(HaveOccurred())
				payload := layoutPackage().Package.Payload[0]
				path := "/v2/" + inspectRepo + "/blobs/" + payload.Digest.String()
				cancelled, cancel := context.WithCancel(ctx)
				DeferCleanup(cancel)
				client := *reg.Client()
				client.Transport = layoutCancelTransport{base: client.Transport, path: path, cancel: cancel}
				target, err := NewRemote(ref, RemoteOptions{HTTPClient: &client})
				Expect(err).NotTo(HaveOccurred())
				in, err := Inspect(ctx, target)
				Expect(err).NotTo(HaveOccurred())
				reg.ResetRequests()

				err = Layout(cancelled, in, root, "pkg")
				Expect(inspectRequests(reg, "", "blobs")).To(ConsistOf(
					And(HaveField("Method", http.MethodGet), HaveField("Path", path), HaveField("Status", http.StatusOK))))
				return err
			},
			func(err error) {
				expectSentinel(err, errNoSentinel)
				Expect(err).To(MatchError(context.Canceled))
			}),
	)

	It("replaces a destination holding a directory of mode 0o555, leaving nothing beside it", func() {
		first := layoutSource(slices.Concat(layoutPayload, []extractEntry{
			extractDir("ro", 0o555),
			extractFile("ro/child", 0o644, "child"),
		})...)
		Expect(Layout(ctx, layoutInspect("first", layoutSpec(first)), root, "pkg")).To(Succeed())
		Expect(extractTree(filepath.Join(rootDir, "pkg"))).To(HaveKeyWithValue("ro", "dr-xr-xr-x"))

		second := layoutSource(layoutPayload...)
		Expect(Layout(ctx, layoutInspect("second", layoutSpec(second)), root, "pkg")).To(Succeed())
		Expect(extractTree(filepath.Join(rootDir, "pkg"))).To(Equal(extractTree(second)))
		Expect(layoutNames(rootDir)).To(ConsistOf("pkg"))
	})

	It("leaves exactly one of two packages laid out concurrently into one destination through one root", func() {
		a := layoutSource(layoutPayload...)
		b := layoutSource(slices.Concat(layoutPayload, []extractEntry{extractFile("only-b", 0o644, "b")})...)
		treeA, treeB := extractTree(a), extractTree(b)
		Expect(treeA).NotTo(Equal(treeB))
		inspections := []*Inspection{layoutInspect("a", layoutSpec(a)), layoutInspect("b", layoutSpec(b))}
		dest := filepath.Join(rootDir, "pkg")

		for round := range 20 {
			// Half the rounds race to create the destination, half to replace it.
			if round%2 == 0 {
				Expect(os.RemoveAll(dest)).To(Succeed())
			}
			errs := make(chan error, len(inspections))
			for _, in := range inspections {
				go func() { errs <- Layout(ctx, in, root, "pkg") }()
			}
			for range inspections {
				Expect(<-errs).To(Succeed(), "round %d", round)
			}
			Expect(extractTree(dest)).To(Or(Equal(treeA), Equal(treeB)), "round %d", round)
			Expect(layoutNames(rootDir)).To(ConsistOf("pkg"), "round %d", round)
		}
	})

	DescribeTable("compares config.json with the metadata config as JSON",
		func(metadata, file string, equal bool) {
			spec := layoutSpec(layoutSource(extractFile("config.json", 0o644, file)))
			spec.Config = json.RawMessage(metadata)
			err := Layout(ctx, layoutInspect("1.0", spec), root, "pkg")
			if equal {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			extractExpectReason(err, artifact.ReasonConfigMismatch)
			Expect(layoutNames(rootDir)).To(BeEmpty())
		},
		Entry("accepting reordered keys", `{"a":1,"b":{"c":[true,null,"s"],"d":"x"}}`, `{"b":{"d":"x","c":[true,null,"s"]},"a":1}`, true),
		Entry("accepting other whitespace", `{"a":[1,2]}`, " {\n\t\"a\" : [ 1 ,\n 2 ]\n}\n", true),
		Entry("accepting 1 as 1.0", `{"n":1}`, `{"n":1.0}`, true),
		Entry("accepting 1 as 1e0", `{"n":1}`, `{"n":1e0}`, true),
		Entry("accepting 1.0 as 1e0", `{"n":1.0}`, `{"n":1e0}`, true),
		Entry("rejecting a changed value", `{"a":1,"b":"x"}`, `{"a":1,"b":"y"}`, false),
		Entry("rejecting a changed number", `{"n":1}`, `{"n":1.5}`, false),
		Entry("rejecting a number that differs beyond float64 precision", `{"n":9007199254740993}`, `{"n":9007199254740992}`, false),
		Entry("rejecting a string in place of a number", `{"n":1}`, `{"n":"1"}`, false),
		Entry("rejecting an extra key", `{"a":1}`, `{"a":1,"b":null}`, false),
		Entry("rejecting data after the value", `{"a":1}`, `{"a":1} {}`, false),
	)

	It("lays out the same tree from a registry and from an OCI layout", func() {
		src := layoutSource(layoutPayload...)
		spec := layoutSpec(src)
		fromRegistry := layoutInspect("1.0", spec)

		dir := GinkgoT().TempDir()
		store, err := oci.New(dir)
		Expect(err).NotTo(HaveOccurred())
		_, _, err = registrytest.PushPackage(ctx, store, "1.0", spec)
		Expect(err).NotTo(HaveOccurred())
		ref, err := NewReference(inspectRepo, "1.0", "")
		Expect(err).NotTo(HaveOccurred())
		target, err := NewLayout(ctx, dir, ref)
		Expect(err).NotTo(HaveOccurred())
		fromLayout, err := Inspect(ctx, target)
		Expect(err).NotTo(HaveOccurred())
		Expect(fromLayout.Descriptor).To(Equal(fromRegistry.Descriptor))

		Expect(Layout(ctx, fromRegistry, root, "registry")).To(Succeed())
		Expect(Layout(ctx, fromLayout, root, "layout")).To(Succeed())
		Expect(extractTree(filepath.Join(rootDir, "registry"))).To(Equal(extractTree(src)))
		Expect(extractTree(filepath.Join(rootDir, "layout"))).To(Equal(extractTree(filepath.Join(rootDir, "registry"))))
	})

	DescribeTable("fails a destination that exists and is not a directory, leaving it untouched",
		func(seed func(dest string)) {
			in := layoutPackage()
			layoutWrite(filepath.Join(rootDir, "real"), layoutSeed...)
			seed(filepath.Join(rootDir, "pkg"))
			before := extractTree(rootDir)

			err := Layout(ctx, in, root, "pkg")
			Expect(err).To(MatchError(ContainSubstring("not a directory")))
			Expect(extractTree(rootDir)).To(Equal(before))
		},
		Entry("a file", func(dest string) { Expect(os.WriteFile(dest, []byte("file"), 0o600)).To(Succeed()) }),
		Entry("a symlink to a directory", func(dest string) { Expect(os.Symlink("real", dest)).To(Succeed()) }),
		Entry("a dangling symlink", func(dest string) { Expect(os.Symlink("missing", dest)).To(Succeed()) }),
	)

	DescribeTable("fails a destination that is not a local path beneath the root before touching the filesystem or the registry",
		func(dest string) {
			in := layoutPackage()
			reg.ResetRequests()
			Expect(Layout(ctx, in, root, dest)).To(MatchError(ContainSubstring("not a local path beneath the root")))
			Expect(layoutNames(base)).To(ConsistOf("root"))
			Expect(layoutNames(rootDir)).To(BeEmpty())
			Expect(reg.Requests()).To(BeEmpty())
		},
		Entry("empty", ""),
		Entry("the root itself", "."),
		Entry("the root itself, before cleaning", filepath.Join("pkg", "..")),
		Entry("absolute", string(filepath.Separator)+"pkg"),
		Entry("leading out of the root", filepath.Join("..", "pkg")),
	)

	It("fails an inspection that is not a package before touching the filesystem or the registry", func() {
		_, err := registrytest.PushImage(ctx, reg.Repository(inspectRepo), "1.0", layoutSource(layoutPayload...))
		Expect(err).NotTo(HaveOccurred())
		in, err := Inspect(ctx, inspectRemote(reg, Credential{}, "1.0", ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(in.Kind).To(Equal(artifact.KindLegacy))
		reg.ResetRequests()

		Expect(Layout(ctx, in, root, "pkg")).To(MatchError(ContainSubstring("not a package")))
		Expect(layoutNames(rootDir)).To(BeEmpty())
		Expect(reg.Requests()).To(BeEmpty())
	})
})
