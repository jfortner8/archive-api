// Package subjecttypes is the catalogue of subject kinds - person, pet, and
// whatever else an archive turns out to be about.
//
// It is deliberately thinner than itemtypes. A subject has no files, so no
// slots and no presentation primitives; a kind is a label plus the set of
// descriptive attributes it declares. The attribute machinery itself is
// shared with items, because "a small validated bag of fields this kind of
// thing has" is the same problem in both places.
package subjecttypes

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
)

//go:embed manifests/*.yaml
var manifestFS embed.FS

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Kind is one entry in the catalogue.
type Kind struct {
	ID    string `yaml:"id" json:"id"`
	Label string `yaml:"label" json:"label"`

	// Plural is what a nav tab says - "People", "Pets". Worth declaring
	// rather than deriving, because English pluralisation is not a rule.
	Plural  string `yaml:"plural" json:"plural"`
	Version int    `yaml:"version" json:"version"`
	Icon    string `yaml:"icon" json:"icon"`

	// Attributes are descriptive only. Nothing here is read by the family
	// tree: a subject's gender does not position it, order it, or decide
	// what it may be related to.
	Attributes []itemtypes.Attribute `yaml:"attributes" json:"attributes,omitempty"`
}

// Attribute looks up one declared attribute.
func (k *Kind) Attribute(id string) (*itemtypes.Attribute, bool) {
	for i := range k.Attributes {
		if k.Attributes[i].ID == id {
			return &k.Attributes[i], true
		}
	}
	return nil, false
}

// Registry is the loaded catalogue, immutable after Load.
type Registry struct {
	byID  map[string]*Kind
	order []string
}

func Load(fsys fs.FS, dir string) (*Registry, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read manifest dir: %w", err)
	}

	reg := &Registry{byID: map[string]*Kind{}}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		path := dir + "/" + entry.Name()
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		var kind Kind
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&kind); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := validate(&kind); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, dup := reg.byID[kind.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate kind id %q", path, kind.ID)
		}

		reg.byID[kind.ID] = &kind
		reg.order = append(reg.order, kind.ID)
	}

	if len(reg.order) == 0 {
		return nil, fmt.Errorf("no manifests found in %s", dir)
	}
	sort.Strings(reg.order)
	return reg, nil
}

// Default is the catalogue compiled into the binary. It panics on a bad
// manifest for the same reason itemtypes does: validating writes against the
// wrong rules is worse than failing to boot.
var Default = func() *Registry {
	reg, err := Load(manifestFS, "manifests")
	if err != nil {
		panic("subjecttypes: " + err.Error())
	}
	return reg
}()

func (r *Registry) Get(id string) (*Kind, bool) {
	kind, ok := r.byID[id]
	return kind, ok
}

func (r *Registry) All() []*Kind {
	out := make([]*Kind, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}

func (r *Registry) IDs() []string {
	return append([]string(nil), r.order...)
}

func validate(k *Kind) error {
	if !idPattern.MatchString(k.ID) {
		return fmt.Errorf("kind id %q must be lower-case kebab-case", k.ID)
	}
	if k.Label == "" || k.Plural == "" {
		return fmt.Errorf("kind %q: label and plural are both required", k.ID)
	}
	if k.Version < 1 {
		return fmt.Errorf("kind %q: version must be at least 1", k.ID)
	}

	seen := map[string]bool{}
	for i := range k.Attributes {
		attr := &k.Attributes[i]

		if !idPattern.MatchString(attr.ID) {
			return fmt.Errorf("kind %q: attribute id %q must be lower-case kebab-case", k.ID, attr.ID)
		}
		if seen[attr.ID] {
			return fmt.Errorf("kind %q: attribute %q declared twice", k.ID, attr.ID)
		}
		seen[attr.ID] = true

		if err := itemtypes.ValidateAttributeDecl(attr); err != nil {
			return fmt.Errorf("kind %q: attribute %q: %w", k.ID, attr.ID, err)
		}
	}
	return nil
}
