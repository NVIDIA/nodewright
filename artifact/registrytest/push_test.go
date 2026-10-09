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

package registrytest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/NVIDIA/nodewright/artifact"
)

const packageConfig = `{"schema_version":"v1","package_name":"hello","package_version":"1.0.0","modes":{}}`

// packageDir writes a package tree: config.json, a script, a subdirectory and
// a symlink.
func packageDir() string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	Expect(os.WriteFile(filepath.Join(dir, "config.json"), []byte(packageConfig), 0o644)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "hello.sh"), []byte("#!/bin/sh\necho hello\n"), 0o755)).To(Succeed())
	Expect(os.Mkdir(filepath.Join(dir, "etc"), 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "etc", "motd"), []byte("hello\n"), 0o600)).To(Succeed())
	Expect(os.Symlink("hello.sh", filepath.Join(dir, "link"))).To(Succeed())
	return dir
}

// fetch classifies the manifest tagged tag in repository name.
func fetch(reg *Registry, name, tag string) (artifact.Kind, *artifact.Manifest, []byte) {
	GinkgoHelper()
	resp, manifest := request(reg, http.MethodGet, "/v2/"+name+"/manifests/"+tag)
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	kind, m, err := artifact.Classify(resp.Header.Get("Content-Type"), manifest)
	Expect(err).NotTo(HaveOccurred())
	return kind, m, manifest
}

// fetchPackage fetches, classifies and parses the package tagged tag.
func fetchPackage(reg *Registry, name, tag string) *artifact.Package {
	GinkgoHelper()
	kind, m, _ := fetch(reg, name, tag)
	Expect(kind).To(Equal(artifact.KindPackage))
	resp, blob := request(reg, http.MethodGet, "/v2/"+name+"/blobs/"+m.Config().Digest.String())
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	pkg, err := m.ParseMetadata(blob)
	Expect(err).NotTo(HaveOccurred())
	return pkg
}

var _ = Describe("builders", func() {
	var (
		ctx context.Context
		reg *Registry
		dir string
	)

	BeforeEach(func() {
		ctx = context.Background()
		reg = New(Options{})
		DeferCleanup(reg.Close)
		dir = packageDir()
	})

	It("push a package that classifies and parses", func() {
		_, _, err := PushPackage(ctx, reg.Repository("pkg"), "v1", PackageSpec{
			Uninstall: artifact.UninstallSupported,
			Payload:   []Layer{{Dir: dir}},
		})
		Expect(err).NotTo(HaveOccurred())

		pkg := fetchPackage(reg, "pkg", "v1")
		Expect(pkg.Uninstall).To(Equal(artifact.UninstallSupported))
		Expect(pkg.Config).To(MatchJSON(packageConfig))
		Expect(pkg.Interrupt).To(BeNil())
		Expect(pkg.Payload).To(HaveLen(1))
		Expect(pkg.HostFiles).To(BeEmpty())
	})

	It("push a package with an interrupt and a host-files layer", func() {
		hostDir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(hostDir, "motd"), []byte("host\n"), 0o644)).To(Succeed())
		crafted, err := TarGzip(hostDir)
		Expect(err).NotTo(HaveOccurred())
		interrupt := &artifact.Interrupt{
			Sense: artifact.InterruptStep{Path: "interrupt/sense.sh", Arguments: []string{}, OnHost: true},
			Run:   artifact.InterruptStep{Path: "interrupt/run.sh", Arguments: []string{"--now"}, OnHost: false},
		}

		_, m, err := PushPackage(ctx, reg.Repository("pkg"), "v1", PackageSpec{
			Uninstall: artifact.UninstallUnsupported,
			Interrupt: interrupt,
			Payload:   []Layer{{Dir: dir}, {Gzip: crafted}},
			HostFiles: []HostFilesLayer{{Layer: Layer{Dir: hostDir}, Destination: "/etc/nodewright"}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.ArtifactType).To(Equal(artifact.ArtifactTypePackage))
		payload, err := TarGzip(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Layers).To(HaveLen(3))
		Expect(m.Layers[0].Digest).To(Equal(digest.FromBytes(payload)))
		Expect(m.Layers[1].Digest).To(Equal(digest.FromBytes(crafted)))
		Expect(m.Layers[2].MediaType).To(Equal(artifact.MediaTypeHostFiles))

		pkg := fetchPackage(reg, "pkg", "v1")
		Expect(pkg.Uninstall).To(Equal(artifact.UninstallUnsupported))
		Expect(pkg.Interrupt).To(Equal(interrupt))
		Expect(pkg.Payload).To(Equal(m.Layers[:2]))
		Expect(pkg.HostFiles).To(HaveLen(1))
		Expect(pkg.HostFiles[0].Destination).To(Equal("/etc/nodewright"))
	})

	It("push a package whose Config overrides config.json", func() {
		crafted, err := TarGzip(dir)
		Expect(err).NotTo(HaveOccurred())
		_, _, err = PushPackage(ctx, reg.Repository("pkg"), "v1", PackageSpec{
			Uninstall: artifact.UninstallSupported,
			Config:    json.RawMessage(`{"package_name":"override"}`),
			Payload:   []Layer{{Gzip: crafted}, {Dir: dir}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fetchPackage(reg, "pkg", "v1").Config).To(MatchJSON(`{"package_name":"override"}`))
	})

	DescribeTable("refuse a package spec",
		func(spec func() PackageSpec) {
			_, _, err := PushPackage(ctx, reg.Repository("pkg"), "v1", spec())
			Expect(err).To(HaveOccurred())
		},
		Entry("with neither Config nor a directory payload layer", func() PackageSpec {
			return PackageSpec{Payload: []Layer{{Gzip: []byte("gzip")}}}
		}),
		Entry("with a layer that sets both Dir and Gzip", func() PackageSpec {
			return PackageSpec{Payload: []Layer{{Dir: dir, Gzip: []byte("gzip")}}}
		}),
		Entry("with a layer that sets neither", func() PackageSpec {
			return PackageSpec{Config: json.RawMessage(`{}`), Payload: []Layer{{}}}
		}),
	)

	It("push a package twice through one Pusher", func() {
		repo := reg.Repository("pkg")
		spec := PackageSpec{Uninstall: artifact.UninstallSupported, Payload: []Layer{{Dir: dir}}}
		first, _, err := PushPackage(ctx, repo, "v1", spec)
		Expect(err).NotTo(HaveOccurred())
		second, _, err := PushPackage(ctx, repo, "v1", spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(Equal(first))
	})

	It("give one directory one digest", func() {
		spec := PackageSpec{Uninstall: artifact.UninstallSupported, Payload: []Layer{{Dir: dir}}}
		first, _, err := PushPackage(ctx, reg.Repository("one"), "v1", spec)
		Expect(err).NotTo(HaveOccurred())
		later := time.Now().Add(time.Hour)
		Expect(os.Chtimes(filepath.Join(dir, "hello.sh"), later, later)).To(Succeed())
		second, _, err := PushPackage(ctx, reg.Repository("two"), "v1", spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Digest).To(Equal(first.Digest))
	})

	It("pack a directory in lexical order with no times or owners", func() {
		data, err := TarGzip(dir)
		Expect(err).NotTo(HaveOccurred())
		zr, err := gzip.NewReader(bytes.NewReader(data))
		Expect(err).NotTo(HaveOccurred())
		tr := tar.NewReader(zr)
		var names []string
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			Expect(err).NotTo(HaveOccurred())
			names = append(names, hdr.Name)
			Expect(hdr.ModTime.Unix()).To(BeZero(), hdr.Name)
			Expect([]int{hdr.Uid, hdr.Gid}).To(Equal([]int{0, 0}), hdr.Name)
			Expect(hdr.Uname+hdr.Gname).To(BeEmpty(), hdr.Name)
			info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(hdr.Name)))
			Expect(err).NotTo(HaveOccurred())
			Expect(hdr.FileInfo().Mode()).To(Equal(info.Mode().Type()|info.Mode().Perm()), hdr.Name)
			if hdr.Typeflag == tar.TypeSymlink {
				Expect(hdr.Linkname).To(Equal("hello.sh"))
			}
		}
		Expect(names).To(Equal([]string{"config.json", "etc/", "etc/motd", "hello.sh", "link"}))
	})

	It("push a legacy image", func() {
		_, err := PushImage(ctx, reg.Repository("img"), "v1", dir)
		Expect(err).NotTo(HaveOccurred())
		kind, m, _ := fetch(reg, "img", "v1")
		Expect(kind).To(Equal(artifact.KindLegacy))
		Expect(m).To(BeNil())
	})

	It("push an index over two platforms", func() {
		_, err := PushIndex(ctx, reg.Repository("img"), "v1", dir)
		Expect(err).NotTo(HaveOccurred())
		kind, _, data := fetch(reg, "img", "v1")
		Expect(kind).To(Equal(artifact.KindLegacy))

		var index ocispec.Index
		Expect(json.Unmarshal(data, &index)).To(Succeed())
		var platforms []string
		for _, desc := range index.Manifests {
			platforms = append(platforms, desc.Platform.OS+"/"+desc.Platform.Architecture)
			kind, _, _ := fetch(reg, "img", desc.Digest.String())
			Expect(kind).To(Equal(artifact.KindLegacy))
		}
		Expect(platforms).To(Equal([]string{"linux/amd64", "linux/arm64"}))
	})
})
