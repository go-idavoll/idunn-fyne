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

package packmodel

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-idavoll/idunn/core/release"
	"github.com/go-idavoll/idunn/core/stage"
)

// Problem is one validation finding, addressed to the field that caused it.
//
// Field uses the packer's own wording ("targets[0].files[1].dst"), so that an
// operator who later reads the packer's message recognises this one.
type Problem struct {
	Field   string
	Message string
}

func (p Problem) String() string { return p.Field + ": " + p.Message }

// Validate reports every problem it can find, in field order.
//
// idunn's own validator stops at the first, because a publish is all-or-nothing
// and the first refusal is the whole answer. A form is different: showing one
// problem at a time makes an operator fix one field per round trip, so this
// collects them all.
//
// An empty result is not a promise that the publish will succeed — the packer is
// the authority, and several of its rules (a src that does not exist, two files
// with identical content, a repository whose root has expired) can only be
// checked when it runs.
func (c *Config) Validate() []Problem {
	var out []Problem
	add := func(field, format string, args ...any) {
		out = append(out, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if !nameRe.MatchString(c.Name) {
		add("name", "must start with a letter or digit and use only letters, digits, "+
			"dot, underscore or hyphen (at most 128 characters)")
	}

	switch {
	case c.Version == "":
		add("version", "required")
	case !release.ValidVersion(c.Version):
		add("version", "%q is not SemVer (want MAJOR.MINOR.PATCH)", c.Version)
	case leadingZeroRe.MatchString(versionCore(c.Version)):
		// "01.2.0" and "1.3.0" are the same release line to a reader and two
		// different delegated roles to the repository.
		add("version", "%q has a leading zero; SemVer forbids it and it would "+
			"split the release line", c.Version)
	}

	switch {
	case c.Channel == "":
		add("channel", "required")
	case !channelRe.MatchString(c.Channel):
		add("channel", "must be lower-case letters, digits and hyphens, starting "+
			"and ending with a letter or digit")
	case lineRoleRe.MatchString(c.Channel):
		add("channel", "%q collides with the delegated role that owns the %s "+
			"release line; pick another name", c.Channel, c.Channel)
	}

	if c.Rollout < 0 || c.Rollout > 1 {
		add("rollout", "%v is outside [0,1]", c.Rollout)
	}

	for _, r := range []struct{ field, value string }{
		{"requirements.min_from_version", c.Requirements.MinFromVersion},
		{"requirements.min_client_version", c.Requirements.MinClientVersion},
	} {
		if r.value != "" && !release.ValidVersion(r.value) {
			add(r.field, "%q is not SemVer", r.value)
		}
	}

	if len(c.Targets) == 0 {
		add("targets", "at least one platform is required")
	}

	seenPlatform := make(map[string]int, len(c.Targets))
	for i := range c.Targets {
		p := &c.Targets[i]
		at := fmt.Sprintf("targets[%d]", i)

		if !platformRe.MatchString(p.OS) {
			add(at+".os", "must be 1 to 32 lower-case letters or digits, e.g. linux")
		}
		if !platformRe.MatchString(p.Arch) {
			add(at+".arch", "must be 1 to 32 lower-case letters or digits, e.g. amd64")
		}
		key := p.OS + "-" + p.Arch
		if first, dup := seenPlatform[key]; dup {
			add(at, "duplicate platform %s; it is already targets[%d]", key, first)
		} else {
			seenPlatform[key] = i
		}

		if len(p.Files) == 0 {
			add(at+".files", "at least one file is required")
		}
		seenDst := make(map[string]int, len(p.Files))
		for j := range p.Files {
			out = append(out, validateFile(&p.Files[j], i, j)...)
			dst := p.Files[j].Dst
			if first, dup := seenDst[dst]; dup {
				add(fmt.Sprintf("%s.files[%d].dst", at, j),
					"duplicate destination %q; it is already files[%d]", dst, first)
			} else if dst != "" {
				seenDst[dst] = j
			}
		}
	}
	return out
}

func validateFile(f *File, pi, fi int) []Problem {
	var out []Problem
	at := fmt.Sprintf("targets[%d].files[%d]", pi, fi)
	add := func(suffix, format string, args ...any) {
		out = append(out, Problem{Field: at + suffix, Message: fmt.Sprintf(format, args...)})
	}

	if f.Src == "" {
		add(".src", "required: the file to publish")
	}

	// stage.SanitizeDst is the same implementation the packer applies and the
	// client runs on ingest, so this is the real rule rather than a copy of it.
	switch clean, err := stage.SanitizeDst(f.Dst); {
	case f.Dst == "":
		add(".dst", "required: where the file is installed, relative to the install root")
	case err != nil:
		add(".dst", "%v", err)
	case clean != f.Dst:
		add(".dst", "%q is not in clean form; write it as %q", f.Dst, clean)
	}

	switch release.FileKind(f.Kind) {
	case release.KindExe, release.KindLib, release.KindData:
	default:
		add(".kind", "must be one of %s", strings.Join(Kinds(), ", "))
	}

	if f.Mode != "" {
		switch {
		case !modeRe.MatchString(f.Mode):
			add(".mode", "%q is not three or four octal digits, e.g. 0755", f.Mode)
		default:
			bits, err := strconv.ParseUint(f.Mode, 8, 32)
			if err != nil {
				add(".mode", "%q is not an octal number", f.Mode)
			} else if bits&^permMask != 0 {
				// setuid, setgid and the sticky bit cannot be expressed at all:
				// refusing them is louder than dropping them silently.
				add(".mode", "%q sets bits outside 0777; setuid, setgid and the "+
					"sticky bit cannot be published", f.Mode)
			}
		}
	}
	return out
}

// versionCore strips the pre-release and build metadata, leaving MAJOR.MINOR.PATCH.
// Mirrors internal/packer/config.go.
func versionCore(v string) string {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return v[:i]
	}
	return v
}
