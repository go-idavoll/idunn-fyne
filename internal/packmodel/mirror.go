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

import "regexp"

// MIRRORED FROM idunn — DO NOT PUT ANYTHING ELSE IN THIS FILE.
//
// github.com/go-idavoll/idunn/internal/packer cannot be imported from another
// module, and these values are unexported there in any case. They are copied so
// the assistant can reject, in the form, exactly what the packer would reject
// when it runs.
//
// When upstream changes one of them this file is wrong, and the assistant will
// either accept a configuration that does not publish or — worse — refuse one
// that does, which pushes an operator to edit the YAML by hand and stop using
// the validator at all. The rejection table in validate_test.go is transcribed
// from internal/packer/config_test.go so that drift shows up as a failing test
// rather than as a puzzled publisher.
//
// TODO(idunn): ask upstream to export a validator — packer.ValidateConfig([]byte)
// error would delete this file outright.
var (
	// nameRe mirrors internal/packer/config.go: the application name.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

	// channelRe mirrors internal/packer/config.go: a DNS-label-ish channel name.
	channelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

	// platformRe mirrors internal/packer/config.go: an os or arch token.
	platformRe = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

	// modeRe mirrors internal/packer/config.go: three or four octal digits.
	modeRe = regexp.MustCompile(`^[0-7]{3,4}$`)

	// leadingZeroRe mirrors internal/packer/config.go, applied to the version
	// core only: "01.2.0" is not the same number to every reader, so it is not
	// allowed to be a version.
	leadingZeroRe = regexp.MustCompile(`^0[0-9]|\.0[0-9]`)

	// lineRoleRe mirrors internal/packer/delegation.go: a channel may not be
	// named like a release-line role ("v1", "v2"), or the two would collide in
	// the delegation namespace.
	lineRoleRe = regexp.MustCompile(`^v[0-9]+$`)
)

// MaxConfigLen mirrors internal/packer.MaxConfigLen: the largest pack.yaml the
// packer will read.
const MaxConfigLen = 1 << 20

// permMask is the bits a mode may set. Anything outside it — setuid, setgid,
// sticky — is refused rather than quietly dropped.
const permMask = 0o777
