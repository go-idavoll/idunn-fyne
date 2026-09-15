// Copyright 2026 The idunn Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package fixture builds releases in memory and serves them through idunn's
// updater.Resolver interface.
//
// It exists so the adapter and the demo can drive a REAL update — real staging,
// a real journal, a real atomic swap, real garbage collection and the real
// Observer events — with no TUF repository, no signing keys and no network.
//
// It stands exactly where core/trust stands, and that is the point: by the time
// the updater sees a descriptor or a target, whether to trust it has already
// been settled (core/updater/updater.go:48-58). Nothing here verifies anything
// and nothing here needs a key, because verification is not this layer's job in
// the real system either.
//
// It is not a test double for the trust client. Anything that wants to know
// whether TUF works must use core/trust against a real repository.
package fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/go-idavoll/idunn/core/release"
)

// Name is the application name every fixture release carries.
const Name = "demo"

// Channel is the channel every fixture release is published on.
const Channel = "stable"

// File is one file of a fixture release, before it is turned into a content
// addressed target.
type File struct {
	Dst  string
	Kind release.FileKind
	Mode uint32
}

// Layout is the file set every fixture release ships. The contents differ per
// version and per destination, deliberately: the idunn packer refuses a release
// in which two destinations hold identical bytes, because content-addressed
// targets would collide (internal/packer/publish.go:230-236). A fixture that
// could not be published is not worth demonstrating.
func Layout() []File {
	return []File{
		{Dst: "bin/app", Kind: release.KindExe, Mode: 0o755},
		{Dst: "lib/libdemo.so", Kind: release.KindLib, Mode: 0o644},
		{Dst: "share/notes.txt", Kind: release.KindData, Mode: 0o644},
	}
}

// Build returns the descriptor for one release together with the payload bytes
// its targets resolve to.
//
// Targets are laid out the way the idunn packer lays them out —
// payloads/v<major>/<sha256 of the content> (internal/packer/delegation.go:109)
// — so a tree built from a fixture looks like a tree built from a real
// repository.
func Build(version, goos, goarch string, req release.Requirements) (*release.Descriptor, map[string][]byte) {
	major := version
	if i := strings.IndexByte(major, '.'); i >= 0 {
		major = major[:i]
	}

	d := &release.Descriptor{
		SchemaVersion: release.SchemaVersion,
		Name:          Name,
		Version:       version,
		Channel:       Channel,
		OS:            goos,
		Arch:          goarch,
		Requirements:  req,
		LayoutSchema:  release.LayoutSchema,
	}
	blobs := make(map[string][]byte, len(Layout()))

	for _, f := range Layout() {
		content := []byte(fmt.Sprintf(
			"%s %s\n%s\n\nThis file is a fixture payload. It differs per version and per\n"+
				"destination so that no two targets share a hash.\n",
			Name, version, f.Dst))
		sum := sha256.Sum256(content)
		target := fmt.Sprintf("payloads/v%s/%s", major, hex.EncodeToString(sum[:]))

		blobs[target] = content
		d.Files = append(d.Files, release.FileRef{
			Target: target,
			Dst:    f.Dst,
			Mode:   f.Mode,
			Kind:   f.Kind,
		})
	}
	return d, blobs
}

// Resolver serves fixture releases. It satisfies updater.Resolver, and through
// that also core/stage.Materializer, because updater.New hands the same object
// to its stager (core/updater/updater.go:295).
//
// The zero value is not usable; call NewResolver.
type Resolver struct {
	mu sync.Mutex

	latest map[string]*release.Descriptor // channel/goos-goarch -> descriptor
	byPath map[string][]byte

	// RefreshErr, when set, is returned by every Refresh. It is how the demo
	// shows what an unreachable update service looks like.
	RefreshErr error

	// LatestErr, when set, is returned by every LatestRelease.
	LatestErr error

	// Tamper, when set, rewrites the bytes a target resolves to, for Target and
	// therefore for Materialize. call counts from 1 per target.
	//
	// VerifyStream is deliberately not routed through it: it answers against the
	// bytes that were published, which is where a signed hash stands. A Tamper
	// that fires is therefore exactly the thing VerifyAfterApply exists to
	// catch — staging writes what it was handed, and the post-apply re-read
	// refuses it.
	Tamper func(target string, call int, data []byte) []byte

	calls map[string]int

	// Refreshes counts calls to Refresh, so a test can assert the updater
	// really did consult the resolver.
	Refreshes int
}

// NewResolver returns an empty Resolver.
func NewResolver() *Resolver {
	return &Resolver{
		latest: make(map[string]*release.Descriptor),
		byPath: make(map[string][]byte),
		calls:  make(map[string]int),
	}
}

// Publish adds a release and makes it the head of its channel. Calling it again
// with a newer version is what "a new release appeared" looks like here.
func (r *Resolver) Publish(d *release.Descriptor, blobs map[string][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.latest[key(d.Channel, d.OS, d.Arch)] = d
	for path, data := range blobs {
		r.byPath[path] = data
	}
}

// Refresh reports whether the update service could be consulted.
func (r *Resolver) Refresh() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.Refreshes++
	return r.RefreshErr
}

// LatestRelease returns the head of a channel, or nil when the channel has none.
func (r *Resolver) LatestRelease(channel, goos, goarch string) (*release.Descriptor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.LatestErr != nil {
		return nil, r.LatestErr
	}
	d, ok := r.latest[key(channel, goos, goarch)]
	if !ok {
		return nil, fmt.Errorf("fixture: no release on channel %q for %s-%s", channel, goos, goarch)
	}
	return d, nil
}

// Target returns the bytes of one target.
func (r *Resolver) Target(targetPath string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, ok := r.byPath[targetPath]
	if !ok {
		return nil, fmt.Errorf("fixture: no such target %q", targetPath)
	}
	r.calls[targetPath]++

	out := make([]byte, len(data))
	copy(out, data)
	if r.Tamper != nil {
		out = r.Tamper(targetPath, r.calls[targetPath], out)
	}
	return out, nil
}

// Materialize streams the bytes of one target into w.
//
// It is the streaming half of updater.Resolver that core/stage consumes in place
// of Target, so a release goes past in a copy window rather than arriving as one
// allocation per file (idunn IDN-12). The fixture verifies nothing here for the
// same reason it verifies nothing anywhere: by the time the updater asks for a
// target, whether to trust it has already been settled upstream.
func (r *Resolver) Materialize(targetPath string, w io.Writer) error {
	data, err := r.Target(targetPath)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("fixture: writing target %q: %w", targetPath, err)
	}
	return nil
}

// TargetLength returns the length a target was published with, without counting
// as a read. It is the signed length in the real system, so it is taken from the
// published bytes and never from what Tamper would hand out — a tampered payload
// of the wrong size has to be answerable as wrong.
func (r *Resolver) TargetLength(targetPath string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, ok := r.byPath[targetPath]
	if !ok {
		return 0, fmt.Errorf("fixture: no such target %q", targetPath)
	}
	return int64(len(data)), nil
}

// VerifyStream reports whether rd yields exactly the bytes a target was
// published with.
//
// This is the one verdict the fixture does give, and it stands where the signed
// hash stands: bytes that did not come from Materialize — a file reused from an
// installed version, an installed file re-read by VerifyAfterApply — are
// admitted only by this.
func (r *Resolver) VerifyStream(targetPath string, rd io.Reader) error {
	r.mu.Lock()
	want, ok := r.byPath[targetPath]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("fixture: no such target %q", targetPath)
	}

	got, err := io.ReadAll(io.LimitReader(rd, int64(len(want))+1))
	if err != nil {
		return fmt.Errorf("fixture: reading target %q: %w", targetPath, err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("fixture: target %q does not match the published bytes", targetPath)
	}
	return nil
}

func key(channel, goos, goarch string) string {
	return channel + "/" + goos + "-" + goarch
}
