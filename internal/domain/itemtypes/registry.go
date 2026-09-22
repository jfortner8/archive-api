package itemtypes

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed manifests/*.yaml
var manifestFS embed.FS

// idPattern keeps type, slot and attribute ids URL- and filename-safe, since
// they appear in query strings, S3 keys and generated TypeScript unions.
var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Registry is the loaded catalogue. It is immutable after Load, so the
// package-level Default can be shared by every request without locking.
type Registry struct {
	byID  map[string]*Type
	order []string
}

// Load reads and validates the embedded manifests. Every caller in the
// service uses Default instead; this exists so tests can load a fixture set.
func Load(fsys fs.FS, dir string) (*Registry, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read manifest dir: %w", err)
	}

	reg := &Registry{byID: map[string]*Type{}}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		path := dir + "/" + entry.Name()
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		var t Type
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		// Unknown fields are an error, matching how the HTTP layer treats
		// unknown request fields. A typo in a manifest key would otherwise
		// silently do nothing, which is the worst way to learn that a slot
		// you thought you declared doesn't exist.
		dec.KnownFields(true)
		if err := dec.Decode(&t); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}

		if err := validate(&t); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, dup := reg.byID[t.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate type id %q", path, t.ID)
		}

		reg.byID[t.ID] = &t
		reg.order = append(reg.order, t.ID)
	}

	if len(reg.order) == 0 {
		return nil, fmt.Errorf("no manifests found in %s", dir)
	}
	sort.Strings(reg.order)
	return reg, nil
}

// Default is the catalogue compiled into the binary. It panics on a malformed
// manifest, which is deliberate: a bad catalogue means uploads would be
// validated against the wrong rules, so failing at startup is far better than
// serving a subtly wrong contract.
var Default = func() *Registry {
	reg, err := Load(manifestFS, "manifests")
	if err != nil {
		panic("itemtypes: " + err.Error())
	}
	return reg
}()

// Get returns the type with this id.
func (r *Registry) Get(id string) (*Type, bool) {
	t, ok := r.byID[id]
	return t, ok
}

// All returns every type, ordered by id so responses and generated code are
// byte-stable across builds.
func (r *Registry) All() []*Type {
	out := make([]*Type, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}

// IDs returns every type id, ordered.
func (r *Registry) IDs() []string {
	return append([]string(nil), r.order...)
}

func validate(t *Type) error {
	if !idPattern.MatchString(t.ID) {
		return fmt.Errorf("type id %q must be lower-case kebab-case", t.ID)
	}
	if t.Label == "" {
		return fmt.Errorf("type %q: label is required", t.ID)
	}
	if t.Version < 1 {
		return fmt.Errorf("type %q: version must be at least 1", t.ID)
	}
	if len(t.Slots) == 0 {
		return fmt.Errorf("type %q: at least one slot is required", t.ID)
	}

	t.slotsByID = map[string]*Slot{}
	for i := range t.Slots {
		slot := &t.Slots[i]

		if !idPattern.MatchString(slot.ID) {
			return fmt.Errorf("type %q: slot id %q must be lower-case kebab-case", t.ID, slot.ID)
		}
		if slot.Label == "" {
			return fmt.Errorf("type %q: slot %q: label is required", t.ID, slot.ID)
		}
		if len(slot.Accepts) == 0 {
			return fmt.Errorf("type %q: slot %q: accepts must list at least one media family", t.ID, slot.ID)
		}
		for _, fam := range slot.Accepts {
			if !isKnownFamily(fam) {
				return fmt.Errorf("type %q: slot %q: unknown media family %q", t.ID, slot.ID, fam)
			}
		}
		if slot.Max < 1 {
			return fmt.Errorf("type %q: slot %q: max must be at least 1 (there is no unlimited)", t.ID, slot.ID)
		}
		if slot.Min < 0 || slot.Min > slot.Max {
			return fmt.Errorf("type %q: slot %q: min must be between 0 and max", t.ID, slot.ID)
		}
		if slot.MaxSizeBytes < 0 {
			return fmt.Errorf("type %q: slot %q: maxSizeBytes cannot be negative", t.ID, slot.ID)
		}

		for _, name := range append([]string{slot.ID}, slot.RenamedFrom...) {
			if _, dup := t.slotsByID[name]; dup {
				return fmt.Errorf("type %q: slot id or alias %q declared twice", t.ID, name)
			}
			t.slotsByID[name] = slot
		}
	}

	seenAttr := map[string]bool{}
	for i := range t.Attributes {
		attr := &t.Attributes[i]

		if !idPattern.MatchString(attr.ID) {
			return fmt.Errorf("type %q: attribute id %q must be lower-case kebab-case", t.ID, attr.ID)
		}
		if seenAttr[attr.ID] {
			return fmt.Errorf("type %q: attribute %q declared twice", t.ID, attr.ID)
		}
		seenAttr[attr.ID] = true

		if !knownAttrKinds[attr.Kind] {
			return fmt.Errorf("type %q: attribute %q: unknown type %q", t.ID, attr.ID, attr.Kind)
		}
		if attr.Kind == AttrEnum && len(attr.Options) == 0 {
			return fmt.Errorf("type %q: attribute %q: enum needs options", t.ID, attr.ID)
		}
		if attr.Kind != AttrEnum && len(attr.Options) > 0 {
			return fmt.Errorf("type %q: attribute %q: options only apply to an enum", t.ID, attr.ID)
		}
		if (attr.Min != nil || attr.Max != nil) && attr.Kind != AttrInteger && attr.Kind != AttrNumber {
			return fmt.Errorf("type %q: attribute %q: min/max only apply to a number", t.ID, attr.ID)
		}
		if attr.Min != nil && attr.Max != nil && *attr.Min > *attr.Max {
			return fmt.Errorf("type %q: attribute %q: min is greater than max", t.ID, attr.ID)
		}
	}

	if len(t.Cover.Prefer) == 0 {
		return fmt.Errorf("type %q: cover.prefer must name at least one slot", t.ID)
	}
	for _, id := range t.Cover.Prefer {
		slot, ok := t.slotsByID[id]
		if !ok {
			return fmt.Errorf("type %q: cover.prefer names unknown slot %q", t.ID, id)
		}
		// A cover has to be something that can actually be rendered as a
		// thumbnail. Preferring an audio slot would leave the gallery tile
		// blank with no obvious cause.
		if !slot.AcceptsFamily(FamilyImage) && !slot.AcceptsFamily(FamilyPDF) {
			return fmt.Errorf("type %q: cover.prefer slot %q holds no image or pdf", t.ID, id)
		}
	}

	return validatePresentation(t)
}

// validatePresentation enforces that every slot is rendered exactly once.
//
// "At least once" would allow a slot's files to be drawn twice; "at most
// once" would allow a slot whose files nothing displays. Deprecated slots are
// included on purpose - they are hidden from capture, but an archive still
// has to show what it was given years ago.
func validatePresentation(t *Type) error {
	if len(t.Presentation) == 0 {
		return fmt.Errorf("type %q: presentation is required", t.ID)
	}

	rendered := map[string]string{} // slot id -> primitive that renders it

	for _, p := range t.Presentation {
		if !knownPrimitives[p.Primitive] {
			return fmt.Errorf("type %q: unknown primitive %q", t.ID, p.Primitive)
		}
		if len(p.Slots) == 0 {
			return fmt.Errorf("type %q: primitive %q names no slots", t.ID, p.Primitive)
		}

		for _, id := range p.Slots {
			slot, ok := t.slotsByID[id]
			if !ok {
				return fmt.Errorf("type %q: primitive %q names unknown slot %q", t.ID, p.Primitive, id)
			}
			if slot.ID != id {
				return fmt.Errorf("type %q: primitive %q should name slot %q, not its alias %q", t.ID, p.Primitive, slot.ID, id)
			}
			if by, dup := rendered[id]; dup {
				return fmt.Errorf("type %q: slot %q is rendered twice (%s and %s)", t.ID, id, by, p.Primitive)
			}
			rendered[id] = string(p.Primitive)
		}

		// Structural expectations a viewer relies on. Getting these wrong
		// produces a viewer that renders nothing or renders the wrong file,
		// with no error anywhere, so they are worth catching at startup.
		switch p.Primitive {
		case PrimitiveSingleImage, PrimitiveVideo, PrimitiveModel3D:
			if len(p.Slots) != 1 {
				return fmt.Errorf("type %q: primitive %q takes exactly one slot", t.ID, p.Primitive)
			}
			if slot := t.slotsByID[p.Slots[0]]; slot.Max != 1 {
				return fmt.Errorf("type %q: primitive %q needs a slot with max 1, but %q allows %d", t.ID, p.Primitive, slot.ID, slot.Max)
			}
		case PrimitiveFlippable:
			if len(p.Slots) != 2 {
				return fmt.Errorf("type %q: primitive %q takes exactly two slots (a front and a back)", t.ID, p.Primitive)
			}
			for _, id := range p.Slots {
				if slot := t.slotsByID[id]; slot.Max != 1 {
					return fmt.Errorf("type %q: primitive %q needs slots with max 1, but %q allows %d", t.ID, p.Primitive, slot.ID, slot.Max)
				}
			}
		}
	}

	for i := range t.Slots {
		if _, ok := rendered[t.Slots[i].ID]; !ok {
			return fmt.Errorf("type %q: slot %q is never rendered - add it to presentation", t.ID, t.Slots[i].ID)
		}
	}
	return nil
}

func isKnownFamily(fam MediaFamily) bool {
	for _, known := range allFamilies {
		if known == fam {
			return true
		}
	}
	return false
}
