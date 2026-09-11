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
	"strings"
	"testing"

	"github.com/go-idavoll/idunn-fyne/internal/packmodel"
)

// good is the minimal configuration every rejection case below mutates. It is
// the same one idunn's own config_test.go starts from.
const good = `name: demo
version: 1.2.0
channel: stable
targets:
  - os: linux
    arch: amd64
    files:
      - {src: app, dst: bin/app, kind: exe}
`

// TestValidateRejectsWhatThePackerRejects is the drift guard.
//
// Every case is transcribed from internal/packer/config_test.go, because the
// rules in mirror.go are copies of unexported upstream values and nothing else
// would notice when they stop matching. Drift in the permissive direction lets
// the assistant wave through a configuration the packer then refuses; drift the
// other way is worse, because an operator who cannot express a valid release in
// the form will edit the YAML by hand and stop using the validator at all.
//
// Two upstream cases are absent on purpose and covered in TestUnmarshal
// instead — "unknown key" and "two documents" are decoder rules, not field
// rules, so they cannot be reached through a Config that already parsed.
func TestValidateRejectsWhatThePackerRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		field string // the field the problem must be addressed to
		want  string // a fragment of the message
	}{
		{"no name", strings.Replace(good, "name: demo\n", "", 1), "name", "must start with"},
		{"not semver", strings.Replace(good, "1.2.0", "1.2", 1), "version", "not SemVer"},
		{"leading zero major", strings.Replace(good, "1.2.0", "01.2.0", 1), "version", "leading zero"},
		{"leading zero patch", strings.Replace(good, "1.2.0", "1.2.03", 1), "version", "leading zero"},
		{"bad requirement", good + "requirements:\n  min_from_version: latest\n",
			"requirements.min_from_version", "not SemVer"},
		{"empty channel", strings.Replace(good, "channel: stable", `channel: ""`, 1), "channel", "required"},
		{"glob channel", strings.Replace(good, "channel: stable", `channel: "*"`, 1), "channel", "lower-case"},
		{"channel with slash", strings.Replace(good, "channel: stable", "channel: a/b", 1), "channel", "lower-case"},
		{"release-line channel", strings.Replace(good, "channel: stable", "channel: v1", 1), "channel", "collides"},
		{"rollout above one", good + "rollout: 1.5\n", "rollout", "outside [0,1]"},
		{"rollout below zero", good + "rollout: -0.5\n", "rollout", "outside [0,1]"},
		{"no targets", "name: demo\nversion: 1.2.0\nchannel: stable\ntargets: []\n", "targets", "at least one"},
		{"no files", strings.Replace(good,
			"    files:\n      - {src: app, dst: bin/app, kind: exe}\n", "    files: []\n", 1),
			"targets[0].files", "at least one"},
		{"bad os", strings.Replace(good, "os: linux", "os: Linux", 1), "targets[0].os", "lower-case"},
		{"bad arch", strings.Replace(good, "arch: amd64", "arch: x86_64!", 1), "targets[0].arch", "lower-case"},
		{"empty src", strings.Replace(good, "src: app", `src: ""`, 1), "targets[0].files[0].src", "required"},
		{"traversal dst", strings.Replace(good, "dst: bin/app", "dst: ../../etc/passwd", 1),
			"targets[0].files[0].dst", "escapes the install root"},
		{"absolute dst", strings.Replace(good, "dst: bin/app", "dst: /etc/passwd", 1),
			"targets[0].files[0].dst", "absolute"},
		{"windows device dst", strings.Replace(good, "dst: bin/app", "dst: NUL", 1),
			"targets[0].files[0].dst", "reserved device name"},
		{"unclean dst", strings.Replace(good, "dst: bin/app", "dst: bin//app", 1),
			"targets[0].files[0].dst", "clean form"},
		{"unknown kind", strings.Replace(good, "kind: exe", "kind: script", 1),
			"targets[0].files[0].kind", "must be one of"},
		{"setuid mode", strings.Replace(good, "kind: exe", `kind: exe, mode: "4755"`, 1),
			"targets[0].files[0].mode", "outside 0777"},
		{"non-octal mode", strings.Replace(good, "kind: exe", `kind: exe, mode: "999"`, 1),
			"targets[0].files[0].mode", "octal digits"},
		{"duplicate dst", strings.Replace(good,
			"      - {src: app, dst: bin/app, kind: exe}\n",
			"      - {src: app, dst: bin/app, kind: exe}\n      - {src: app2, dst: bin/app, kind: data}\n", 1),
			"targets[0].files[1].dst", "duplicate destination"},
		{"duplicate platform",
			good + "  - os: linux\n    arch: amd64\n    files:\n      - {src: app, dst: x, kind: data}\n",
			"targets[1]", "duplicate platform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := packmodel.Unmarshal([]byte(tc.body))
			if err != nil {
				t.Fatalf("the case body does not parse: %v", err)
			}
			problems := c.Validate()
			if len(problems) == 0 {
				t.Fatalf("accepted a configuration the packer would refuse\n%s", tc.body)
			}
			for _, p := range problems {
				if p.Field == tc.field && strings.Contains(p.Message, tc.want) {
					return
				}
			}
			t.Errorf("no problem on %q containing %q; got %v", tc.field, tc.want, problems)
		})
	}
}

func TestValidateAcceptsAGoodConfiguration(t *testing.T) {
	c, err := packmodel.Unmarshal([]byte(good))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if problems := c.Validate(); len(problems) != 0 {
		t.Errorf("rejected a valid configuration: %v", problems)
	}
}

// TestValidateAcceptsTheDocumentedExample runs the example from idunn's own
// docs/packer.md through the validator. If the assistant refused the published
// documentation, it would be the assistant that is wrong.
func TestValidateAcceptsTheDocumentedExample(t *testing.T) {
	const doc = `name: acme-app
version: 1.3.0
channel: stable
requirements:
  min_from_version: 1.0.0
  min_client_version: 1.2.0
rollout: 0.1
targets:
  - os: windows
    arch: amd64
    files:
      - {src: build/win-amd64/app.exe, dst: app.exe, kind: exe}
      - {src: build/win-amd64/plugin.dll, dst: lib/plugin.dll, kind: lib}
  - os: linux
    arch: amd64
    files:
      - {src: build/linux-amd64/app, dst: app, kind: exe}
      - {src: build/linux-amd64/libx.so, dst: lib/libx.so, kind: lib}
`
	c, err := packmodel.Unmarshal([]byte(doc))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if problems := c.Validate(); len(problems) != 0 {
		t.Errorf("rejected the example from idunn's own docs: %v", problems)
	}
}

// TestValidateReportsEveryProblem is the difference from the packer: a form has
// to show everything at once, or an operator fixes one field per round trip.
func TestValidateReportsEveryProblem(t *testing.T) {
	c := &packmodel.Config{
		Name:    "",
		Version: "nope",
		Channel: "V1",
		Rollout: 9,
		Targets: []packmodel.Platform{{
			OS:   "Linux",
			Arch: "",
			Files: []packmodel.File{
				{Src: "", Dst: "../escape", Kind: "script", Mode: "8888"},
			},
		}},
	}
	problems := c.Validate()
	if len(problems) < 8 {
		t.Fatalf("reported %d problems, want every one of them: %v", len(problems), problems)
	}

	fields := map[string]bool{}
	for _, p := range problems {
		fields[p.Field] = true
		if p.Message == "" {
			t.Errorf("problem on %q has no message", p.Field)
		}
	}
	for _, want := range []string{
		"name", "version", "channel", "rollout",
		"targets[0].os", "targets[0].arch",
		"targets[0].files[0].src", "targets[0].files[0].dst",
		"targets[0].files[0].kind", "targets[0].files[0].mode",
	} {
		if !fields[want] {
			t.Errorf("no problem reported for %q; got %v", want, problems)
		}
	}
}

// TestValidateIsAdvisoryNotAVerdict pins the thing the doc comment promises: a
// clean form says nothing about whether src exists or whether two files share
// content, both of which only the packer can answer.
func TestValidateIsAdvisoryNotAVerdict(t *testing.T) {
	c := &packmodel.Config{
		Name: "demo", Version: "1.0.0", Channel: "stable",
		Targets: []packmodel.Platform{{
			OS: "linux", Arch: "amd64",
			Files: []packmodel.File{
				{Src: "definitely/not/on/this/disk", Dst: "bin/app", Kind: "exe"},
			},
		}},
	}
	if problems := c.Validate(); len(problems) != 0 {
		t.Errorf("Validate opened the filesystem; it must judge the text only: %v", problems)
	}
}
