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
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ErrEmit is a configuration that could not be turned into a pack.yaml the
// packer would read back.
var ErrEmit = errors.New("packmodel: emit")

// Marshal renders c as pack.yaml.
//
// It re-reads its own output with KnownFields set before returning it, which is
// what the packer's decoder does: an unknown key there is a hard error, not a
// warning. That round trip is cheap and it makes it impossible for this package
// to write a document the packer would refuse to parse — a class of bug an
// operator would otherwise meet only at publish time.
func Marshal(c *Config) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: no configuration", ErrEmit)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEmit, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEmit, err)
	}

	out := buf.Bytes()
	if len(out) > MaxConfigLen {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d the packer will read",
			ErrEmit, len(out), MaxConfigLen)
	}

	var back Config
	dec := yaml.NewDecoder(bytes.NewReader(out))
	dec.KnownFields(true)
	if err := dec.Decode(&back); err != nil {
		return nil, fmt.Errorf("%w: the emitted document does not parse the way the "+
			"packer parses it: %w", ErrEmit, err)
	}
	return out, nil
}

// Unmarshal reads a pack.yaml back into a Config, the way the packer reads it:
// an unknown key is an error, and exactly one document is allowed.
//
// The assistant uses it to open a file a publisher already has, so that editing
// an existing release does not mean retyping it.
func Unmarshal(raw []byte) (*Config, error) {
	if len(raw) > MaxConfigLen {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d the packer will read",
			ErrEmit, len(raw), MaxConfigLen)
	}

	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEmit, err)
	}

	// The packer refuses a second document rather than silently using the first.
	var extra Config
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("%w: pack.yaml holds more than one YAML document", ErrEmit)
	}
	return &c, nil
}
