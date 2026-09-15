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

// Package packmodel is the pack.yaml a publisher is building, together with the
// validation and the emitter the packing assistant needs.
//
// # The packer is the authority
//
// github.com/go-idavoll/idunn/internal/packer cannot be imported from another
// module, so this package cannot call the real validator. What it does instead
// is reject, before the packer runs, what the packer would reject — using the
// same public helpers where idunn exports them (core/stage.SanitizeDst,
// core/release.ValidVersion) and mirrored copies where it does not (mirror.go).
//
// A configuration this package accepts is therefore not a promise that the
// publish will succeed. Only the packer decides that, and the assistant must
// never present a green form as if it were a verdict.
package packmodel

import (
	"github.com/go-idavoll/idunn/core/release"
)

// Config is one pack.yaml.
//
// The fields and their YAML names mirror internal/packer.Config exactly. Mode is
// a string rather than an integer for the reason idunn gives: YAML's own integer
// rules make a leading zero mean different things in different parsers, and a
// permission bit is not a place for that ambiguity.
type Config struct {
	Name         string       `yaml:"name"`
	Version      string       `yaml:"version"`
	Channel      string       `yaml:"channel"`
	Requirements Requirements `yaml:"requirements"`
	Rollout      float64      `yaml:"rollout,omitempty"`
	Targets      []Platform   `yaml:"targets"`
}

// Requirements are the floors a client must be at before this release applies.
type Requirements struct {
	MinFromVersion   string `yaml:"min_from_version,omitempty"`
	MinClientVersion string `yaml:"min_client_version,omitempty"`
}

// Platform is one os/arch pair and the files published for it.
type Platform struct {
	OS    string `yaml:"os"`
	Arch  string `yaml:"arch"`
	Files []File `yaml:"files"`
}

// File is one published file.
type File struct {
	// Src is the file to publish, relative to pack.yaml unless absolute.
	Src string `yaml:"src"`

	// Dst is the install-relative destination. It is validated with the same
	// sanitizer the client runs on ingest, so a path that would be refused at
	// install time is refused here instead of shipping.
	Dst string `yaml:"dst"`

	// Kind is one of exe, lib, data.
	Kind string `yaml:"kind"`

	// Mode is an optional explicit POSIX mode in octal ("0644"). Empty means the
	// default for Kind.
	Mode string `yaml:"mode,omitempty"`
}

// DefaultMode is the mode a kind gets when Mode is empty. It mirrors idunn's
// defaultModes (internal/packer/config.go).
func DefaultMode(kind string) string {
	if release.FileKind(kind) == release.KindExe {
		return "0755"
	}
	return "0644"
}

// Kinds lists the file kinds the packer accepts, for a chooser. They come from
// core/release rather than from a copied list, so they cannot drift.
func Kinds() []string {
	return []string{
		string(release.KindExe),
		string(release.KindLib),
		string(release.KindData),
	}
}

// New returns a Config with the shape a publisher usually starts from: one
// platform for the machine they are on, with no files yet.
func New(goos, goarch string) *Config {
	return &Config{
		Channel: "stable",
		Targets: []Platform{{OS: goos, Arch: goarch}},
	}
}
