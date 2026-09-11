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

package packmodel_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-idavoll/idunn-fyne/internal/packmodel"
)

func sample() *packmodel.Config {
	return &packmodel.Config{
		Name:    "demo",
		Version: "1.2.0",
		Channel: "stable",
		Requirements: packmodel.Requirements{
			MinFromVersion:   "1.0.0",
			MinClientVersion: "1.1.0",
		},
		Rollout: 0.1,
		Targets: []packmodel.Platform{{
			OS: "linux", Arch: "amd64",
			Files: []packmodel.File{
				{Src: "build/app", Dst: "bin/app", Kind: "exe"},
				{Src: "build/lib.so", Dst: "lib/lib.so", Kind: "lib", Mode: "0644"},
			},
		}},
	}
}

// TestMarshalQuotesTheMode is the one formatting detail that matters. idunn
// keeps mode as a string precisely because YAML's integer rules make a leading
// zero mean different things in different parsers, and an unquoted 0644 would
// hand that ambiguity straight back.
func TestMarshalQuotesTheMode(t *testing.T) {
	out, err := packmodel.Marshal(sample())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), `mode: "0644"`) {
		t.Errorf("mode is not quoted:\n%s", out)
	}
}

// TestMarshalOmitsWhatWasNotSet keeps the emitted file readable: a publisher who
// set no rollout should not find one written down.
func TestMarshalOmitsWhatWasNotSet(t *testing.T) {
	c := &packmodel.Config{
		Name: "demo", Version: "1.0.0", Channel: "stable",
		Targets: []packmodel.Platform{{
			OS: "linux", Arch: "amd64",
			Files: []packmodel.File{{Src: "app", Dst: "bin/app", Kind: "exe"}},
		}},
	}
	out, err := packmodel.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, unwanted := range []string{"rollout", "mode:", "min_from_version", "min_client_version"} {
		if strings.Contains(string(out), unwanted) {
			t.Errorf("emitted %q for a field that was never set:\n%s", unwanted, out)
		}
	}
}

// TestMarshalRoundTrips is the self-check the emitter performs internally, made
// visible: what this package writes, it can read back under the packer's own
// strict rules.
func TestMarshalRoundTrips(t *testing.T) {
	want := sample()
	out, err := packmodel.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := packmodel.Unmarshal(out)
	if err != nil {
		t.Fatalf("Unmarshal of our own output: %v", err)
	}
	if got.Name != want.Name || got.Version != want.Version || got.Channel != want.Channel ||
		got.Rollout != want.Rollout || len(got.Targets) != len(want.Targets) {
		t.Errorf("round trip changed the configuration:\ngot  %+v\nwant %+v", got, want)
	}
	if len(got.Targets[0].Files) != 2 || got.Targets[0].Files[1].Mode != "0644" {
		t.Errorf("round trip lost file detail: %+v", got.Targets[0].Files)
	}
}

// TestMarshalIsDeterministic: the packer's whole contract is that two runs over
// the same inputs produce a byte-identical repository, and an emitter that
// reordered keys would break that before the packer ever saw it.
func TestMarshalIsDeterministic(t *testing.T) {
	first, err := packmodel.Marshal(sample())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := packmodel.Marshal(sample())
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first, again)
		}
	}
}

func TestMarshalRefusesNil(t *testing.T) {
	if _, err := packmodel.Marshal(nil); !errors.Is(err, packmodel.ErrEmit) {
		t.Errorf("Marshal(nil) = %v, want ErrEmit", err)
	}
}

// TestUnmarshalRefusesAnUnknownKey covers the decoder rule the packer applies:
// an unknown key is a hard error, so a typo cannot silently publish something
// other than what was meant.
func TestUnmarshalRefusesAnUnknownKey(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"unknown top-level key", strings.Replace(good, "channel: stable", "channels: stable", 1)},
		{"unknown nested key", strings.Replace(good, "kind: exe", "kind: exe, sudo: true", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := packmodel.Unmarshal([]byte(tc.body)); !errors.Is(err, packmodel.ErrEmit) {
				t.Errorf("Unmarshal accepted an unknown key: %v", err)
			}
		})
	}
}

// TestUnmarshalRefusesASecondDocument mirrors the packer, which refuses rather
// than silently using the first document.
func TestUnmarshalRefusesASecondDocument(t *testing.T) {
	if _, err := packmodel.Unmarshal([]byte(good + "---\nname: other\n")); err == nil {
		t.Error("Unmarshal accepted a file holding two YAML documents")
	}
}

func TestUnmarshalRefusesAnOversizedFile(t *testing.T) {
	huge := make([]byte, packmodel.MaxConfigLen+1)
	for i := range huge {
		huge[i] = '\n'
	}
	if _, err := packmodel.Unmarshal(huge); !errors.Is(err, packmodel.ErrEmit) {
		t.Errorf("Unmarshal accepted %d bytes, past the %d the packer reads",
			len(huge), packmodel.MaxConfigLen)
	}
}

func TestDefaultModeAndKinds(t *testing.T) {
	if got := packmodel.DefaultMode("exe"); got != "0755" {
		t.Errorf("DefaultMode(exe) = %q, want 0755", got)
	}
	for _, kind := range []string{"lib", "data", "anything else"} {
		if got := packmodel.DefaultMode(kind); got != "0644" {
			t.Errorf("DefaultMode(%q) = %q, want 0644", kind, got)
		}
	}
	if got := packmodel.Kinds(); len(got) != 3 {
		t.Errorf("Kinds() = %v, want the three the packer accepts", got)
	}
}

func TestNewStartsFromSomethingUsable(t *testing.T) {
	c := packmodel.New("linux", "amd64")
	if c.Channel != "stable" || len(c.Targets) != 1 {
		t.Fatalf("New() = %+v, want one platform on the stable channel", c)
	}
	// It is deliberately incomplete: there are no files yet, so it must not
	// validate clean and let someone publish an empty release.
	if problems := c.Validate(); len(problems) == 0 {
		t.Error("a brand new, empty configuration validated clean")
	}
}

func TestProblemString(t *testing.T) {
	p := packmodel.Problem{Field: "version", Message: "not SemVer"}
	if got := p.String(); got != "version: not SemVer" {
		t.Errorf("String() = %q", got)
	}
}
