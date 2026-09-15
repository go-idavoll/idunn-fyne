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

package demoui_test

import (
	"runtime"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
	"github.com/go-idavoll/idunn-fyne/fyneui"
	"github.com/go-idavoll/idunn-fyne/internal/demoui"
	"github.com/go-idavoll/idunn-fyne/internal/fixture"
	"github.com/go-idavoll/idunn/core/release"
)

func TestDescribeWithoutARelease(t *testing.T) {
	got := demoui.Describe(nil, "")
	if !strings.Contains(got, "Check for updates") {
		t.Errorf("Describe(nil) = %q, want the prompt to check", got)
	}
}

// TestDescribeShowsEverythingADescriptorCarries is the honest-UI test: the card
// must show every field there is, and say that there are no others. A UI that
// implies missing release notes invites someone to add an unsigned side channel
// to supply them.
func TestDescribeShowsEverythingADescriptorCarries(t *testing.T) {
	d, _ := fixture.Build("1.1.0", "linux", "amd64", release.Requirements{
		MinFromVersion:   "1.0.0",
		MinClientVersion: "1.0.0",
	})
	d.Rollout = 0.25

	got := demoui.Describe(d, "1.0.0")

	for _, want := range []string{
		"demo 1.1.0", "stable", "linux-amd64",
		"Upgrading from 1.0.0",
		"25% of installations",
		"at least 1.0.0",
		"Layout schema 1",
		"bin/app", "lib/libdemo.so", "share/notes.txt",
		"exe", "lib", "data",
		"0755", "0644",
		"no release notes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() is missing %q\n---\n%s", want, got)
		}
	}
}

func TestDescribeNamesAFirstInstall(t *testing.T) {
	d, _ := fixture.Build("1.0.0", "linux", "amd64", release.Requirements{})
	got := demoui.Describe(d, "")
	if !strings.Contains(got, "first install") {
		t.Errorf("Describe(.., \"\") = %q, want it to say this is a first install", got)
	}
	// A rollout of zero means "everyone", not "nobody"; it must not be rendered
	// as a staged rollout of 0%.
	if strings.Contains(got, "Staged rollout") {
		t.Errorf("a zero rollout was rendered as a staged one:\n%s", got)
	}
}

// TestWindowBuildsAndReadsTheInstallation covers the screen's construction and
// its read of a root that has nothing in it yet.
func TestWindowBuildsAndReadsTheInstallation(t *testing.T) {
	test.NewTempApp(t)

	panel := fyneui.NewPanel(nil)
	defer panel.Close()

	root := t.TempDir()
	res := fixture.NewResolver()
	d, blobs := fixture.Build("1.0.0", runtime.GOOS, runtime.GOARCH, release.Requirements{})
	res.Publish(d, blobs)

	w := demoui.New(panel, demoui.Options{
		Root:          root,
		Channel:       fixture.Channel,
		Resolver:      res,
		ClientVersion: "1.0.0",
	})
	if w.Content() == nil {
		t.Fatal("Content() = nil")
	}
	test.NewTempWindow(t, w.Content())
	test.LaidOutObjects(w.Content())
}
