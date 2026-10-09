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
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"

	"github.com/NVIDIA/nodewright/artifact"
	"github.com/NVIDIA/nodewright/artifact/registrytest"
)

// extractEntry is one entry of a crafted layer, with its content when it is
// a regular file.
type extractEntry struct {
	hdr  tar.Header
	body string
}

func extractFile(name string, mode int64, body string) extractEntry {
	return extractEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(body))}, body: body}
}

func extractDir(name string, mode int64) extractEntry {
	return extractEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: mode}}
}

func extractLink(name, target string) extractEntry {
	return extractEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777}}
}

// extractTarBytes writes entries, in order, as an uncompressed tar.
func extractTarBytes(entries ...extractEntry) []byte {
	GinkgoHelper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		Expect(tw.WriteHeader(&e.hdr)).To(Succeed())
		_, err := tw.Write([]byte(e.body))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(tw.Close()).To(Succeed())
	return buf.Bytes()
}

func extractCompress(data []byte) []byte {
	GinkgoHelper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(data)
	Expect(err).NotTo(HaveOccurred())
	Expect(zw.Close()).To(Succeed())
	return buf.Bytes()
}

// extractGzip builds a crafted layer of entries.
func extractGzip(entries ...extractEntry) []byte {
	GinkgoHelper()
	return extractCompress(extractTarBytes(entries...))
}

// extractTree describes everything beneath dir by its slash path: a symlink
// by its target, a directory by its mode, a file by its mode and content.
func extractTree(dir string) map[string]string {
	GinkgoHelper()
	tree := map[string]string{}
	Expect(filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		desc := info.Mode().String()
		switch {
		case d.Type() == fs.ModeSymlink:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			desc = "-> " + target
		case !d.IsDir():
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			desc += " " + string(body)
		}
		tree[filepath.ToSlash(rel)] = desc
		return nil
	})).To(Succeed())
	return tree
}

// extractExpectReason asserts that err is an invalid artifact of reason and
// matches no sentinel.
func extractExpectReason(err error, reason artifact.Reason) {
	GinkgoHelper()
	var invalid *artifact.InvalidArtifactError
	Expect(errors.As(err, &invalid)).To(BeTrue(), "%v", err)
	Expect(invalid.Reason).To(Equal(reason), "%v", err)
	expectSentinel(err, errNoSentinel)
}

var _ = Describe("ExtractLayer", func() {
	var (
		ctx    context.Context
		reg    *registrytest.Registry
		target *Target
		// base holds only outer, which holds only dir, the directory
		// extracted into.
		base string
		dir  string
		root *os.Root
	)

	BeforeEach(func() {
		ctx = context.Background()
		reg = registrytest.New(registrytest.Options{})
		DeferCleanup(reg.Close)
		ref, err := NewReference(reg.Host()+"/test/app", "1.0", "")
		Expect(err).NotTo(HaveOccurred())
		target, err = NewRemote(ref, RemoteOptions{HTTPClient: reg.Client()})
		Expect(err).NotTo(HaveOccurred())

		base = GinkgoT().TempDir()
		dir = filepath.Join(base, "outer", "layer")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		root, err = os.OpenRoot(dir)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(root.Close)
	})

	// extractPush pushes layers as the payload layers of one package and
	// returns their descriptors.
	extractPush := func(layers ...[]byte) []ocispec.Descriptor {
		GinkgoHelper()
		spec := registrytest.PackageSpec{Uninstall: artifact.UninstallSupported, Config: json.RawMessage(`{}`)}
		for _, gz := range layers {
			spec.Payload = append(spec.Payload, registrytest.Layer{Gzip: gz})
		}
		_, m, err := registrytest.PushPackage(ctx, reg.Repository("test/app"), "1.0", spec)
		Expect(err).NotTo(HaveOccurred())
		return m.Layers
	}

	extractRun := func(gz []byte) error {
		GinkgoHelper()
		return ExtractLayer(ctx, target, extractPush(gz)[0], root)
	}

	extractExpectNothingOutside := func() {
		GinkgoHelper()
		for _, d := range []string{base, filepath.Dir(dir)} {
			entries, err := os.ReadDir(d)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(HaveLen(1), d)
		}
	}

	DescribeTable("extracts ./-prefixed names, with symlink targets verbatim, from a layer of either media type",
		func(mediaType string) {
			desc := extractPush(extractGzip(
				extractDir("./etc/", 0o755),
				extractFile("./etc/motd", 0o644, "hello"),
				extractLink("./motd", "etc/motd"),
				extractLink("./hostname", "/etc/hostname"),
				extractLink("./up", "../../.."),
			))[0]
			desc.MediaType = mediaType
			Expect(ExtractLayer(ctx, target, desc, root)).To(Succeed())
			Expect(extractTree(dir)).To(Equal(map[string]string{
				"etc":      "drwxr-xr-x",
				"etc/motd": "-rw-r--r-- hello",
				"motd":     "-> etc/motd",
				"hostname": "-> /etc/hostname",
				"up":       "-> ../../..",
			}))
		},
		Entry("payload", artifact.MediaTypePayload),
		Entry("host-files", artifact.MediaTypeHostFiles),
	)

	It("extracts a path longer than 100 bytes from a PAX extended header, creating its unnamed parent 0o755", func() {
		parent := strings.Repeat("d", 60)
		name := parent + "/" + strings.Repeat("f", 120)
		long := extractFile(name, 0o644, "long")
		long.hdr.Format = tar.FormatPAX
		data := extractTarBytes(long)
		Expect(data[156]).To(BeEquivalentTo(tar.TypeXHeader), "the first header is a PAX extended header")

		Expect(extractRun(extractCompress(data))).To(Succeed())
		Expect(extractTree(dir)).To(Equal(map[string]string{
			parent: "drwxr-xr-x",
			name:   "-rw-r--r-- long",
		}))
	})

	It("accepts the . directory entry and leaves the directory its own mode", func() {
		Expect(os.Chmod(dir, 0o750)).To(Succeed())
		Expect(extractRun(extractGzip(extractDir(".", 0o777), extractFile("file", 0o644, "x")))).To(Succeed())
		info, err := os.Stat(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(fs.FileMode(0o750)))
		Expect(extractTree(dir)).To(Equal(map[string]string{"file": "-rw-r--r-- x"}))
	})

	DescribeTable("rejects an entry that breaks a tar rule, writing nothing outside the directory",
		func(bad extractEntry) {
			err := extractRun(extractGzip(extractFile("first", 0o644, "first"), bad))
			extractExpectReason(err, artifact.ReasonLayerEntry)
			extractExpectNothingOutside()
		},
		Entry("an absolute name", extractFile("/escape", 0o644, "escaped")),
		Entry("a .. segment", extractFile("../escape", 0o644, "escaped")),
		Entry("a hardlink", extractEntry{hdr: tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "first"}}),
		Entry("a character device", extractEntry{hdr: tar.Header{Name: "null", Typeflag: tar.TypeChar, Mode: 0o666, Devmajor: 1, Devminor: 3}}),
		Entry("a PAX global header", extractEntry{hdr: tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "global"}}}),
	)

	DescribeTable("rejects an entry beneath a symlink the layer lays down that escapes, writing nothing outside the directory",
		func(linkTarget string) {
			err := extractRun(extractGzip(extractLink("link", linkTarget), extractFile("link/x", 0o644, "escaped")))
			extractExpectReason(err, artifact.ReasonLayerEntry)
			extractExpectNothingOutside()
			_, err = os.Lstat("/x")
			Expect(err).To(MatchError(fs.ErrNotExist))
			Expect(extractTree(dir)).To(Equal(map[string]string{"link": "-> " + linkTarget}))
		},
		Entry("to ../..", "../.."),
		Entry("to /", "/"),
	)

	It("rejects an entry beneath a symlink that points inside the directory", func() {
		err := extractRun(extractGzip(extractDir("sub", 0o755), extractLink("in", "sub"), extractFile("in/x", 0o644, "x")))
		extractExpectReason(err, artifact.ReasonLayerEntry)
		Expect(extractTree(dir)).To(Equal(map[string]string{"sub": "drwxr-xr-x", "in": "-> sub"}))
	})

	It("rejects an entry beneath a symlink planted before extraction, writing nothing outside the directory", func() {
		outside := filepath.Join(base, "outside")
		Expect(os.Mkdir(outside, 0o755)).To(Succeed())
		Expect(os.Symlink("../../outside", filepath.Join(dir, "planted"))).To(Succeed())

		err := extractRun(extractGzip(extractFile("planted/x", 0o644, "escaped")))
		extractExpectReason(err, artifact.ReasonLayerEntry)
		Expect(extractTree(outside)).To(BeEmpty())
	})

	It("gives files and directories exactly their permission bits, without special bits, after the layer is written", func() {
		DeferCleanup(os.Chmod, filepath.Join(dir, "ro"), fs.FileMode(0o755))
		Expect(extractRun(extractGzip(
			extractFile("all", 0o6777, "all"),
			extractDir("private", 0o1700),
			extractDir("ro", 0o555),
			extractFile("ro/child", 0o644, "child"),
		))).To(Succeed())
		Expect(extractTree(dir)).To(Equal(map[string]string{
			"all":      "-rwxrwxrwx all",
			"private":  "drwx------",
			"ro":       "dr-xr-xr-x",
			"ro/child": "-rw-r--r-- child",
		}))
	})

	It("lays a second layer over the first, replacing whatever is at each path but an existing directory", func() {
		layers := extractPush(
			extractGzip(
				extractFile("file", 0o644, "one"),
				extractFile("f2d", 0o644, "one"),
				extractDir("d2f", 0o755),
				extractFile("d2f/child", 0o644, "one"),
				extractFile("target", 0o644, "target"),
				extractLink("s2f", "target"),
				extractDir("keep", 0o755),
				extractFile("keep/a", 0o644, "one"),
			),
			extractGzip(
				extractFile("file", 0o600, "two"),
				extractDir("f2d", 0o755),
				extractFile("f2d/inner", 0o644, "two"),
				extractFile("d2f", 0o644, "two"),
				extractFile("s2f", 0o644, "two"),
				extractDir("keep", 0o700),
			),
		)
		for _, desc := range layers {
			Expect(ExtractLayer(ctx, target, desc, root)).To(Succeed())
		}
		Expect(extractTree(dir)).To(Equal(map[string]string{
			"file":      "-rw------- two",
			"f2d":       "drwxr-xr-x",
			"f2d/inner": "-rw-r--r-- two",
			"d2f":       "-rw-r--r-- two",
			"target":    "-rw-r--r-- target",
			"s2f":       "-rw-r--r-- two",
			"keep":      "drwx------",
			"keep/a":    "-rw-r--r-- one",
		}))
	})

	DescribeTable("fails a tampered layer with ErrDigestMismatch, before any other error",
		func(tamper func(gz []byte) []byte) {
			gz := extractGzip(extractFile("file", 0o644, "content"))
			desc := extractPush(gz)[0]
			reg.Tamper(desc.Digest, tamper(bytes.Clone(gz)))
			err := ExtractLayer(ctx, target, desc, root)
			expectSentinel(err, ErrDigestMismatch)
			var invalid *artifact.InvalidArtifactError
			Expect(errors.As(err, &invalid)).To(BeFalse(), "%v", err)
		},
		Entry("in its gzip header, at the same length", func(gz []byte) []byte {
			gz[0] ^= 0xff
			return gz
		}),
		Entry("in its gzip trailer, at the same length", func(gz []byte) []byte {
			gz[len(gz)-1] ^= 0xff
			return gz
		}),
		Entry("at a different length", func(gz []byte) []byte {
			return append(gz, 0)
		}),
	)

	DescribeTable("fails a layer that matches its digest but is not a well-formed gzip-compressed tar with ReasonLayerEntry",
		func(layer func() []byte) {
			extractExpectReason(extractRun(layer()), artifact.ReasonLayerEntry)
		},
		Entry("not gzip", func() []byte { return []byte("not a gzip stream") }),
		Entry("gzip of something other than a tar", func() []byte { return extractCompress([]byte("not a tar")) }),
		Entry("a gzip checksum that fails only past the end of the tar", func() []byte {
			// GNU tar pads the archive to its record size, so the tar ends
			// before the gzip stream does.
			data := append(extractTarBytes(extractFile("file", 0o644, "content")), make([]byte, 8192)...)
			gz := extractCompress(data)
			gz[len(gz)-8] ^= 0xff
			return gz
		}),
	)

	It("stops at a done context before laying out an entry and returns its error", func() {
		layoutDir := GinkgoT().TempDir()
		store, err := oci.New(layoutDir)
		Expect(err).NotTo(HaveOccurred())
		_, m, err := registrytest.PushPackage(ctx, store, "1.0", registrytest.PackageSpec{
			Uninstall: artifact.UninstallSupported,
			Config:    json.RawMessage(`{}`),
			Payload:   []registrytest.Layer{{Gzip: extractGzip(extractFile("file", 0o644, "content"))}},
		})
		Expect(err).NotTo(HaveOccurred())
		ref, err := NewReference("ghcr.io/org/app", "1.0", "")
		Expect(err).NotTo(HaveOccurred())
		layout, err := NewLayout(ctx, layoutDir, ref)
		Expect(err).NotTo(HaveOccurred())

		// A layout fetch ignores the context, so only extraction can stop.
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		err = ExtractLayer(cancelled, layout, m.Layers[0], root)
		expectSentinel(err, errNoSentinel)
		Expect(err).To(MatchError(context.Canceled))
		Expect(extractTree(dir)).To(BeEmpty())
	})

	It("refuses a layer of another media type without sending a request", func() {
		desc := extractPush(extractGzip(extractFile("file", 0o644, "content")))[0]
		desc.MediaType = ocispec.MediaTypeImageLayerGzip
		reg.ResetRequests()
		extractExpectReason(ExtractLayer(ctx, target, desc, root), artifact.ReasonMediaType)
		Expect(reg.Requests()).To(BeEmpty())
		Expect(extractTree(dir)).To(BeEmpty())
	})
})
