// Package itemtypes is the archive's catalogue of item types - photo,
// two-sided photo, document, CD, and whatever comes next.
//
// The point of this package is that adding a type is a data change, not a
// code change. An item splits into three parts, and only the third varies:
//
//  1. Common archival metadata (title, date, location, notes, tags, people).
//     Universal and strongly typed, shared by every type. This is what the
//     gallery, map and timeline read, and it is why a CD and a photo can sit
//     side by side on one timeline.
//  2. Files, each filling a named slot declared by the type.
//  3. A small map of type-specific attributes.
//
// Because only the vocabulary of slots and attributes varies, the stored
// shape of an item never changes: one table, one index, one item shape. A new
// type ships as a manifest entry and needs no migration.
//
// See docs/adding-an-item-type.md for the authoring guide.
package itemtypes

import (
	"fmt"
	"strings"
)

// MediaFamily is the broad kind of bytes a file holds. Slots accept families
// rather than individual MIME types so that a new image codec (HEIC today,
// something else tomorrow) doesn't require touching every manifest, and so
// derivative generation has exactly one thing to switch on.
type MediaFamily string

const (
	FamilyImage   MediaFamily = "image"
	FamilyAudio   MediaFamily = "audio"
	FamilyVideo   MediaFamily = "video"
	FamilyPDF     MediaFamily = "pdf"
	FamilyModel3D MediaFamily = "model3d"
)

// allFamilies is also the canonical ordering for Capabilities, so the derived
// list is stable across writes and diffs cleanly.
var allFamilies = []MediaFamily{FamilyImage, FamilyAudio, FamilyVideo, FamilyPDF, FamilyModel3D}

// Primitive names a way of presenting a group of files. Types compose
// primitives instead of getting a bespoke viewer, which is what keeps the
// cost of a new type near zero: a CD is a flip card plus a single image plus
// a page-turner plus an audio player, all of which already exist. New UI code
// is needed only for a genuinely new primitive.
//
// A client that doesn't recognise a primitive falls back to rendering the
// slot's files by media family, so an older UI never breaks on a newer type.
type Primitive string

const (
	// PrimitiveSingleImage shows one image.
	PrimitiveSingleImage Primitive = "single-image"
	// PrimitiveFlippable shows two faces with a flip animation, for anything
	// with a front and a back.
	PrimitiveFlippable Primitive = "flippable"
	// PrimitivePaged shows an ordered sequence as a book spread.
	PrimitivePaged Primitive = "paged"
	// PrimitiveStack shows a loose pile you riffle through, for photos that
	// were digitised together but not yet catalogued apart.
	PrimitiveStack Primitive = "stack"
	// PrimitiveAudio is a waveform player.
	PrimitiveAudio Primitive = "audio"
	// PrimitivePDF renders PDF pages.
	PrimitivePDF Primitive = "pdf"
	// PrimitiveVideo is a player with a poster frame.
	PrimitiveVideo Primitive = "video"
	// PrimitiveModel3D is an interactive 3D viewer.
	PrimitiveModel3D Primitive = "model3d"
)

var knownPrimitives = map[Primitive]bool{
	PrimitiveSingleImage: true, PrimitiveFlippable: true, PrimitivePaged: true,
	PrimitiveStack: true, PrimitiveAudio: true, PrimitivePDF: true,
	PrimitiveVideo: true, PrimitiveModel3D: true,
}

// AttrKind is the type of a type-specific attribute value.
type AttrKind string

const (
	AttrString  AttrKind = "string"
	AttrInteger AttrKind = "integer"
	AttrNumber  AttrKind = "number"
	AttrBool    AttrKind = "boolean"
	AttrEnum    AttrKind = "enum"
)

var knownAttrKinds = map[AttrKind]bool{
	AttrString: true, AttrInteger: true, AttrNumber: true,
	AttrBool: true, AttrEnum: true,
}

// Slot is one named place a file can go: a CD's disc face, a document's
// pages, a photo's back.
//
// Slots are optional by default (Min is 0). "Some of them but not others" is
// the normal case for an archive - a CD with only an MP3 and no photographs
// is still a CD - so requiring a file is the exception that has to be asked
// for explicitly.
type Slot struct {
	ID    string `yaml:"id" json:"id"`
	Label string `yaml:"label" json:"label"`

	// Accepts lists the media families allowed here. Empty is invalid.
	Accepts []MediaFamily `yaml:"accepts" json:"accepts"`

	// Min is how many files this slot requires; zero, and therefore optional,
	// unless stated.
	Min int `yaml:"min" json:"min"`

	// Max is how many files fit. Required, and at least 1 - there is
	// deliberately no "unset means unlimited", because an int whose zero
	// value means something is exactly how order: 0 became indistinguishable
	// from no order at all elsewhere in this codebase.
	Max int `yaml:"max" json:"max"`

	// Ordered says position within the slot is meaningful (booklet pages), as
	// opposed to a set (voice memos).
	Ordered bool `yaml:"ordered" json:"ordered"`

	// MaxSizeBytes caps one file. It is also what tells the client whether a
	// single presigned PUT will do or multipart is needed, so it is part of
	// the contract rather than just a guard. Zero means no slot-specific cap.
	MaxSizeBytes int64 `yaml:"maxSizeBytes" json:"maxSizeBytes,omitempty"`

	// Deprecated hides the slot from capture UI while still rendering files
	// already stored in it. Slots are never deleted - an archive has to keep
	// displaying what it was given years ago.
	Deprecated bool `yaml:"deprecated" json:"deprecated,omitempty"`

	// RenamedFrom lists former ids that should resolve to this slot, so
	// renaming one doesn't strand the files already in it.
	RenamedFrom []string `yaml:"renamedFrom" json:"renamedFrom,omitempty"`
}

// Accepts reports whether this slot takes the given media family.
func (s *Slot) AcceptsFamily(fam MediaFamily) bool {
	for _, a := range s.Accepts {
		if a == fam {
			return true
		}
	}
	return false
}

// Attribute declares one type-specific metadata field - a CD's artist, a
// scan's capture method. Anything universal enough to filter or sort the
// whole archive by belongs in common metadata instead, not here.
type Attribute struct {
	ID      string   `yaml:"id" json:"id"`
	Label   string   `yaml:"label" json:"label"`
	Kind    AttrKind `yaml:"type" json:"type"`
	Options []string `yaml:"options" json:"options,omitempty"` // enum only
	Min     *float64 `yaml:"min" json:"min,omitempty"`
	Max     *float64 `yaml:"max" json:"max,omitempty"`
}

// Presentation maps a group of slots onto one viewer primitive.
type Presentation struct {
	Primitive Primitive `yaml:"primitive" json:"primitive"`
	Slots     []string  `yaml:"slots" json:"slots"`
}

// Cover says which slots to look in, in order, when choosing the image that
// represents an item in the gallery. Server-owned so every surface agrees,
// rather than each client re-deriving it from the file list.
type Cover struct {
	Prefer []string `yaml:"prefer" json:"prefer"`
}

// Type is one entry in the catalogue.
type Type struct {
	ID    string `yaml:"id" json:"id"`
	Label string `yaml:"label" json:"label"`

	// Version is bumped only for a narrowing change - lowering a Max, adding
	// a Min, dropping an accepted family. Adding an optional slot or
	// attribute is always backward compatible and needs no bump.
	Version int `yaml:"version" json:"version"`

	Icon     string `yaml:"icon" json:"icon"`
	Category string `yaml:"category" json:"category"`

	Slots        []Slot         `yaml:"slots" json:"slots"`
	Attributes   []Attribute    `yaml:"attributes" json:"attributes,omitempty"`
	Cover        Cover          `yaml:"cover" json:"cover"`
	Presentation []Presentation `yaml:"presentation" json:"presentation"`

	// slotsByID includes RenamedFrom aliases. Built during Load.
	slotsByID map[string]*Slot
}

// Slot resolves a role to its slot, following RenamedFrom aliases. The second
// return is false for a role this type doesn't define.
func (t *Type) Slot(role string) (*Slot, bool) {
	s, ok := t.slotsByID[role]
	return s, ok
}

// Attribute looks up one declared attribute by id.
func (t *Type) Attribute(id string) (*Attribute, bool) {
	for i := range t.Attributes {
		if t.Attributes[i].ID == id {
			return &t.Attributes[i], true
		}
	}
	return nil, false
}

// Capabilities lists the media families this type can hold, deduplicated and
// in a stable order. Derived rather than declared, stored on the item and
// projected into the index, so that "everything with audio" is a filter
// rather than a scan.
func (t *Type) Capabilities() []MediaFamily {
	var out []MediaFamily
	for _, fam := range allFamilies {
		for i := range t.Slots {
			if t.Slots[i].AcceptsFamily(fam) {
				out = append(out, fam)
				break
			}
		}
	}
	return out
}

// FamilyForContentType maps a MIME type to the family a slot can accept.
// Unknown types are rejected rather than waved through: the upload is refused
// before any bytes move, which is cheaper for everyone than storing something
// nothing can render.
func FamilyForContentType(contentType string) (MediaFamily, error) {
	// Drop any parameters, e.g. "text/plain; charset=utf-8".
	base := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}

	switch {
	case base == "application/pdf":
		return FamilyPDF, nil
	case strings.HasPrefix(base, "image/"):
		return FamilyImage, nil
	case strings.HasPrefix(base, "audio/"):
		return FamilyAudio, nil
	case strings.HasPrefix(base, "video/"):
		return FamilyVideo, nil
	case strings.HasPrefix(base, "model/"):
		return FamilyModel3D, nil
	}
	return "", fmt.Errorf("unsupported content type %q", contentType)
}
