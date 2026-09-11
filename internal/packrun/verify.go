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

package packrun

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/go-idavoll/idunn/core/release"
	"github.com/go-idavoll/idunn/core/trust"
)

// VerifyRequest asks for a published repository to be resolved the way an
// installation resolves it.
type VerifyRequest struct {
	RepoDir string

	// RootJSON is the trust anchor. Empty means "take the highest-numbered
	// <n>.root.json out of the repository", which is right for checking your own
	// publish and wrong for anything else: a repository that vouches for itself
	// proves only that it is internally consistent.
	RootJSON []byte

	Channel string
	OS      string
	Arch    string

	// At overrides the clock. It exists for checking a repository whose metadata
	// has expired, and it is never set on an ordinary verification -- the expiry
	// check is most of the point.
	At time.Time
}

// VerifyResult is what the client saw.
type VerifyResult struct {
	Descriptor *release.Descriptor

	// Bytes maps each destination to the number of verified bytes resolved for
	// it. Every file of the descriptor is fetched, because a repository that
	// resolves a descriptor but cannot produce its payloads is not published.
	Bytes map[string]int

	// SelfAnchored reports that the anchor came out of the repository itself.
	SelfAnchored bool
}

// Verify resolves a published repository end to end with core/trust.
//
// This is the real done-criterion for a publish, and it is why the assistant
// uses the client rather than reading the metadata itself: the question is not
// "did the packer write plausible files" but "can the software this repository
// serves actually take an update from it". Nothing here re-implements a trust
// decision; go-tuf makes all of them, through the same client an installation
// runs.
//
// The repository is served over a loopback HTTP listener because that is the
// transport the client speaks. Nothing leaves the machine.
func Verify(ctx context.Context, req VerifyRequest) (*VerifyResult, error) {
	if req.RepoDir == "" {
		return nil, fmt.Errorf("%w: no repository directory", ErrVerify)
	}

	metaDir := filepath.Join(req.RepoDir, MetadataDir)
	targetDir := filepath.Join(req.RepoDir, TargetsDir)
	for _, d := range []string{metaDir, targetDir} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not a published repository (no %s directory)",
				ErrVerify, req.RepoDir, filepath.Base(d))
		}
	}

	anchor, selfAnchored := req.RootJSON, false
	if len(anchor) == 0 {
		raw, err := latestRoot(metaDir)
		if err != nil {
			return nil, err
		}
		anchor, selfAnchored = raw, true
	}

	mux := http.NewServeMux()
	mux.Handle("/metadata/", http.StripPrefix("/metadata/", http.FileServer(http.Dir(metaDir))))
	mux.Handle("/targets/", http.StripPrefix("/targets/", http.FileServer(http.Dir(targetDir))))

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVerify, err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(listener)
	}()
	defer func() {
		_ = srv.Close()
		<-done
	}()

	base := "http://" + listener.Addr().String()
	cache, err := os.MkdirTemp("", "idunn-verify-*")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVerify, err)
	}
	// A fresh cache every time: a reused one could resolve from what a previous
	// run already trusted, which would prove less than nothing.
	defer func() { _ = os.RemoveAll(cache) }()

	opts := trust.Options{
		Root:        anchor,
		MetadataURL: base + "/metadata/",
		TargetsURL:  base + "/targets/",
		LocalDir:    cache,
	}
	if !req.At.IsZero() {
		at := req.At
		opts.Now = func() time.Time { return at }
	}

	client, err := trust.New(opts)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVerify, err)
	}
	if !req.At.IsZero() {
		client.UnsafeSetRefTime(req.At)
	}

	if err := client.Refresh(); err != nil {
		return nil, fmt.Errorf("%w: the repository metadata did not verify: %w", ErrVerify, err)
	}

	d, err := client.LatestRelease(req.Channel, req.OS, req.Arch)
	if err != nil {
		return nil, fmt.Errorf("%w: no release resolved on channel %q for %s-%s: %w",
			ErrVerify, req.Channel, req.OS, req.Arch, err)
	}

	out := &VerifyResult{Descriptor: d, Bytes: map[string]int{}, SelfAnchored: selfAnchored}
	for _, f := range d.Files {
		raw, err := client.Target(f.Target)
		if err != nil {
			return nil, fmt.Errorf("%w: the descriptor resolved but %s did not: %w",
				ErrVerify, f.Dst, err)
		}
		out.Bytes[f.Dst] = len(raw)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

var rootFileRe = regexp.MustCompile(`^(\d+)\.root\.json$`)

// latestRoot returns the highest-numbered root.json in a metadata directory.
// MIRRORED from internal/packer/state.go, which picks the same file -- and which
// notes that the packer never creates one: the root ceremony does.
func latestRoot(metaDir string) ([]byte, error) {
	entries, err := os.ReadDir(metaDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVerify, err)
	}

	var versions []int
	byVersion := map[int]string{}
	for _, e := range entries {
		m := rootFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		versions = append(versions, n)
		byVersion[n] = e.Name()
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("%w: %s holds no <n>.root.json; the root ceremony creates "+
			"it, the packer never does", ErrVerify, metaDir)
	}
	sort.Ints(versions)

	name := byVersion[versions[len(versions)-1]]
	raw, err := os.ReadFile(filepath.Join(metaDir, name))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVerify, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: %s is empty", ErrVerify, name)
	}
	return raw, nil
}

// SelfAnchoredWarning is what a user interface must say when Verify used the
// repository's own root. It is not a lie detector; it only proves the repository
// is internally consistent.
const SelfAnchoredWarning = "Verified against the repository's own root.json. " +
	"That proves the repository is internally consistent, not that it is signed " +
	"by the keys your installations trust — check that with the anchor those " +
	"installations actually ship."
