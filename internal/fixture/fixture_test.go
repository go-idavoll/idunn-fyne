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

package fixture_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-idavoll/idunn-fyne/internal/fixture"
	"github.com/go-idavoll/idunn/core/release"
	"github.com/go-idavoll/idunn/core/stage"
	"github.com/go-idavoll/idunn/core/updater"
)

// Resolver must satisfy the interfaces idunn will hand it to. updater.New puts
// the same object behind its stager, so both have to hold.
var (
	_ updater.Resolver   = (*fixture.Resolver)(nil)
	_ stage.Materializer = (*fixture.Resolver)(nil)
)

// TestBuildProducesDistinctContentPerFile guards the rule the idunn packer
// enforces: two destinations in one release must not hold identical bytes, or
// their content-addressed targets would collide.
func TestBuildProducesDistinctContentPerFile(t *testing.T) {
	d, blobs := fixture.Build("1.2.0", "linux", "amd64", release.Requirements{})

	if len(d.Files) != len(fixture.Layout()) {
		t.Fatalf("descriptor has %d files, want %d", len(d.Files), len(fixture.Layout()))
	}
	if len(blobs) != len(d.Files) {
		t.Errorf("%d distinct targets for %d files; some files share content",
			len(blobs), len(d.Files))
	}
	for _, f := range d.Files {
		if !strings.HasPrefix(f.Target, "payloads/v1/") {
			t.Errorf("target %q is not laid out the way the packer lays targets out", f.Target)
		}
		// The destinations must survive the same sanitizer the client runs on
		// ingest, or the fixture could never be installed.
		if _, err := stage.SanitizeDst(f.Dst); err != nil {
			t.Errorf("SanitizeDst(%q) = %v", f.Dst, err)
		}
	}
}

// TestBuildVersionsDiffer keeps an upgrade observable: if 1.0.0 and 1.1.0 wrote
// the same bytes, nothing would prove the swap happened.
func TestBuildVersionsDiffer(t *testing.T) {
	_, a := fixture.Build("1.0.0", "linux", "amd64", release.Requirements{})
	_, b := fixture.Build("1.1.0", "linux", "amd64", release.Requirements{})
	for target := range a {
		if _, shared := b[target]; shared {
			t.Errorf("target %q is identical across two versions", target)
		}
	}
}

func TestResolverServesWhatWasPublished(t *testing.T) {
	r := fixture.NewResolver()
	d, blobs := fixture.Build("1.0.0", "linux", "amd64", release.Requirements{})
	r.Publish(d, blobs)

	if err := r.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if r.Refreshes != 1 {
		t.Errorf("Refreshes = %d, want 1", r.Refreshes)
	}

	got, err := r.LatestRelease(fixture.Channel, "linux", "amd64")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if got.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", got.Version)
	}

	for _, f := range d.Files {
		raw, err := r.Target(f.Target)
		if err != nil {
			t.Fatalf("Target(%q): %v", f.Target, err)
		}
		if len(raw) == 0 {
			t.Errorf("Target(%q) returned no bytes", f.Target)
		}
	}
}

func TestResolverReportsWhatItDoesNotHave(t *testing.T) {
	r := fixture.NewResolver()

	if _, err := r.LatestRelease("nightly", "linux", "amd64"); err == nil {
		t.Error("LatestRelease succeeded for a channel with no releases")
	}
	if _, err := r.Target("payloads/v1/missing"); err == nil {
		t.Error("Target succeeded for a path that was never published")
	}
}

func TestResolverInjectedErrors(t *testing.T) {
	r := fixture.NewResolver()
	want := errors.New("the update service is unreachable")

	r.RefreshErr = want
	if err := r.Refresh(); !errors.Is(err, want) {
		t.Errorf("Refresh = %v, want %v", err, want)
	}

	r.LatestErr = want
	if _, err := r.LatestRelease(fixture.Channel, "linux", "amd64"); !errors.Is(err, want) {
		t.Errorf("LatestRelease = %v, want %v", err, want)
	}
}

// TestResolverTamperOnlyOnTheSecondRead is how the demo shows VerifyAfterApply
// catching something: staging gets the real bytes, the post-apply re-read does
// not.
func TestResolverTamperOnlyOnTheSecondRead(t *testing.T) {
	r := fixture.NewResolver()
	d, blobs := fixture.Build("1.0.0", "linux", "amd64", release.Requirements{})
	r.Publish(d, blobs)
	r.Tamper = func(_ string, call int, data []byte) []byte {
		if call == 1 {
			return data
		}
		return []byte("tampered")
	}

	target := d.Files[0].Target
	first, err := r.Target(target)
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if string(first) == "tampered" {
		t.Error("the first read was tampered with; staging would never have succeeded")
	}
	second, err := r.Target(target)
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if string(second) != "tampered" {
		t.Error("the second read was not tampered with")
	}
}

// TestTargetReturnsACopy: a caller that scribbles on the bytes it was given must
// not corrupt the fixture for the next read.
func TestTargetReturnsACopy(t *testing.T) {
	r := fixture.NewResolver()
	d, blobs := fixture.Build("1.0.0", "linux", "amd64", release.Requirements{})
	r.Publish(d, blobs)

	target := d.Files[0].Target
	first, err := r.Target(target)
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	first[0] = 'X'

	second, err := r.Target(target)
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if second[0] == 'X' {
		t.Error("Target handed out the fixture's own slice; one caller can corrupt the next")
	}
}
