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

// Package packrun runs the idunn packer and reads what it said.
//
// github.com/go-idavoll/idunn/internal/packer cannot be imported from another
// module, so publishing is done by running cmd/packer as a subprocess. That is
// not a workaround to be apologised for: the packer owns the repository layout,
// the signing and the reproducibility rules, and a second implementation of any
// of them in a GUI would be a parallel trust path.
//
// # Keys
//
// This package never opens a signing key. It learns that a TUF_* variable is
// set, it may stat the file the value names in order to warn about it, and it
// passes the value through to the child process. Key bytes never enter this
// program's memory, its log, or its user interface (idunn AGENTS.md §5).
package packrun

import (
	"errors"
)

// The failures of a publish. They are sentinels so a user interface can tell a
// refusal apart from a crash without reading English.
var (
	// ErrNotFound is a packer binary that could not be located or is not
	// executable.
	ErrNotFound = errors.New("packrun: no packer binary")

	// ErrKeyEnv is a required role-key variable that is unset, blank, or holds
	// key material instead of a path. It is raised before the child starts.
	ErrKeyEnv = errors.New("packrun: role key")

	// ErrUsage is exit code 2: the packer did not understand the command line.
	// That is a defect in this program, not something the operator did.
	ErrUsage = errors.New("packrun: the packer rejected the command line")

	// ErrPublish is a publish the packer refused or could not complete.
	ErrPublish = errors.New("packrun: publish refused")

	// ErrVerify is a published repository that the real client could not
	// resolve end to end.
	ErrVerify = errors.New("packrun: verification")
)

// MetadataDir and TargetsDir are where a published repository keeps its two
// halves.
//
// MIRRORED from internal/packer/state.go, which is not importable. They are part
// of the on-disk layout a client is served, so they change about as often as the
// layout does -- but if they ever do, verification here looks in the wrong place.
const (
	MetadataDir = "metadata"
	TargetsDir  = "targets"
)
