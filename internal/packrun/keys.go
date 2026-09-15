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
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// The role-key environment variables the packer reads. MIRRORED from
// internal/packer/keys.go.
const (
	EnvTargetsKey   = "TUF_TARGETS_KEY"
	EnvSnapshotKey  = "TUF_SNAPSHOT_KEY"
	EnvTimestampKey = "TUF_TIMESTAMP_KEY"

	// EnvDelegationKeyPrefix names an optional per-delegation override, e.g.
	// TUF_DELEGATION_KEY_STABLE or TUF_DELEGATION_KEY_V2. Unset delegations fall
	// back to the targets key.
	EnvDelegationKeyPrefix = "TUF_DELEGATION_KEY_"
)

// RequiredKeys are the three variables a publish cannot proceed without.
func RequiredKeys() []string {
	return []string{EnvTargetsKey, EnvSnapshotKey, EnvTimestampKey}
}

// DelegationKeyEnv is the variable name that overrides one delegated role's key.
// MIRRORED from internal/packer/keys.go: the role is upper-cased and anything
// outside [A-Z0-9] becomes an underscore.
func DelegationKeyEnv(role string) string {
	var b strings.Builder
	b.WriteString(EnvDelegationKeyPrefix)
	for _, r := range strings.ToUpper(role) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// KeyStatus is everything this program is allowed to know about a signing key:
// that a variable is set, and what path it names.
//
// There is no field for the key itself, and there is no code here that could
// fill one. Ref holds the value as given -- a path, or a file: URI -- and is
// cleared when the value turns out to be key material rather than a reference.
type KeyStatus struct {
	Name     string
	Required bool
	Set      bool
	Ref      string
	Mode     fs.FileMode

	// Material reports that the variable holds a PEM block instead of a path.
	// The packer refuses that outright, for the reason this program then
	// repeats: an environment variable is visible in process listings, crash
	// dumps and CI logs, so a key in one is a key that has leaked.
	Material bool

	// Exists and Readable are the result of a stat, never of a read.
	Exists bool

	// Loose reports a key file other users can read. It is a warning, not a
	// refusal: the packer does not check it, and it is not this program's place
	// to invent a rule the publisher's own tooling does not have.
	Loose bool
}

// Problem describes what is wrong with this key reference, or "" if nothing is.
func (k KeyStatus) Problem() string {
	switch {
	case k.Material:
		return "holds key material, not a path; point it at a file instead — a key in " +
			"an environment variable is visible in process listings and logs"
	case !k.Set && k.Required:
		return "is not set; the packer refuses to publish without it"
	case k.Set && k.Ref == "":
		return "is set but empty"
	case k.Set && !k.Exists:
		return "names a file that is not there"
	default:
		return ""
	}
}

// StatFunc is the only filesystem access this file performs. It is injected so a
// test can prove that nothing here ever opens a key.
type StatFunc func(name string) (fs.FileInfo, error)

// InspectKeyEnv reports the state of the role-key variables.
//
// It never opens a file. It reads the variable, decides whether the value is a
// reference or key material, and stats the path so the interface can say "that
// file is not there" before a publish spends anything finding out.
func InspectKeyEnv(lookup func(string) (string, bool), stat StatFunc, delegations []string) []KeyStatus {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if stat == nil {
		stat = os.Stat
	}

	names := RequiredKeys()
	required := len(names)

	seen := map[string]bool{}
	for _, role := range delegations {
		name := DelegationKeyEnv(role)
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names[required:])

	out := make([]KeyStatus, 0, len(names))
	for i, name := range names {
		k := KeyStatus{Name: name, Required: i < required}

		raw, ok := lookup(name)
		k.Set = ok
		value := strings.TrimSpace(raw)

		switch {
		case !ok:
		case strings.Contains(value, "-----BEGIN"):
			// Deliberately not stored anywhere: the value IS the secret.
			k.Material = true
		default:
			k.Ref = value
			if path := localPath(value); path != "" {
				if info, err := stat(path); err == nil {
					k.Exists = true
					k.Mode = info.Mode()
					k.Loose = info.Mode().Perm()&0o077 != 0
				}
			}
		}
		out = append(out, k)
	}
	return out
}

// localPath turns a key reference into a filesystem path, or "" when it is not
// one this program can stat. MIRRORED from internal/packer/keys.go: a bare path
// stays a path, and only the file: scheme is understood -- which is why
// "C:\keys\targets.pem" must not be mistaken for a URI.
func localPath(ref string) string {
	const scheme = "file:"
	if !strings.HasPrefix(ref, scheme) {
		return ref
	}
	rest := strings.TrimPrefix(ref, scheme)
	rest = strings.TrimPrefix(rest, "//")
	return rest
}

// CheckKeyEnv refuses a publish whose required keys are not usable, before any
// child process starts. Failing here costs nothing; failing halfway through a
// publish costs an operator their confidence in the repository's state.
func CheckKeyEnv(statuses []KeyStatus) error {
	var bad []string
	for _, k := range statuses {
		if !k.Required && !k.Set {
			continue
		}
		if p := k.Problem(); p != "" {
			bad = append(bad, k.Name+" "+p)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrKeyEnv, strings.Join(bad, "; "))
}
