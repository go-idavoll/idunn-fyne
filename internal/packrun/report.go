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
	"strconv"
	"strings"
)

// Report is the structured reading of what a publish printed.
//
// cmd/packer writes human-readable text; it has no machine-readable mode. That
// makes this parser best-effort by nature, and it is written to degrade rather
// than to break: a line it does not recognise is kept verbatim in Unparsed, and
// the exit code -- not this struct -- remains the answer to whether the publish
// succeeded.
//
// TODO(idunn): ask upstream for a --json flag on `packer publish`, which would
// delete this file.
type Report struct {
	Name    string
	Version string
	Channel string

	// Roles maps a metadata role to the version it was written at.
	Roles map[string]int64

	// Delegations maps a delegated role to how many targets it now holds.
	Delegations map[string]int

	// AddedTargets is how many targets this publish added.
	AddedTargets int

	// Published reports that the summary line was seen at all.
	Published bool

	// Unparsed holds every line this parser did not understand, in order, so a
	// user interface can show what it could not summarise instead of hiding it.
	Unparsed []string
}

// ParseReport reads the output of `packer publish`. It never fails: anything it
// cannot place goes to Unparsed.
//
// The shapes it knows, from cmd/packer's report():
//
//	published <name> <version> on channel <channel>
//	  role <role>        -> version <n>
//	  delegation <role>  holds <n> targets
//	  <n> new targets
func ParseReport(stdout string) Report {
	r := Report{
		Roles:       map[string]int64{},
		Delegations: map[string]int{},
	}

	for _, raw := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		f := strings.Fields(line)

		switch {
		// published <name> <version> on channel <channel>
		case f[0] == "published" && len(f) == 6 && f[3] == "on" && f[4] == "channel":
			r.Published = true
			r.Name, r.Version, r.Channel = f[1], f[2], f[5]

		// role <role> -> version <n>
		case f[0] == "role" && len(f) == 5 && f[2] == "->" && f[3] == "version":
			if n, err := strconv.ParseInt(f[4], 10, 64); err == nil {
				r.Roles[f[1]] = n
			} else {
				r.Unparsed = append(r.Unparsed, raw)
			}

		// delegation <role> holds <n> targets
		case f[0] == "delegation" && len(f) == 5 && f[2] == "holds" && f[4] == "targets":
			if n, err := strconv.Atoi(f[3]); err == nil {
				r.Delegations[f[1]] = n
			} else {
				r.Unparsed = append(r.Unparsed, raw)
			}

		// <n> new targets
		case len(f) == 3 && f[1] == "new" && f[2] == "targets":
			if n, err := strconv.Atoi(f[0]); err == nil {
				r.AddedTargets = n
			} else {
				r.Unparsed = append(r.Unparsed, raw)
			}

		default:
			r.Unparsed = append(r.Unparsed, raw)
		}
	}
	return r
}

// Summary renders the report as one line for a status bar. It says "published"
// only when the packer did.
func (r Report) Summary() string {
	if !r.Published {
		return "the packer printed no publish summary"
	}
	var b strings.Builder
	b.WriteString("published " + r.Name + " " + r.Version + " on channel " + r.Channel)
	b.WriteString("; " + strconv.Itoa(r.AddedTargets) + " new target")
	if r.AddedTargets != 1 {
		b.WriteString("s")
	}
	b.WriteString("; " + strconv.Itoa(len(r.Roles)) + " roles signed")
	return b.String()
}
