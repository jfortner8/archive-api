package domain

import (
	"time"

	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
)

// FileStatus tracks whether derivatives have been generated yet. Returned to
// the client so it can render a placeholder during the gap between an upload
// finishing and its thumbnails existing, rather than a broken image.
type FileStatus string

const (
	FileStatusPending FileStatus = "pending"
	FileStatusReady   FileStatus = "ready"
	FileStatusFailed  FileStatus = "failed"
)

// File is one stored blob attached to an item.
//
// Note there is no Order field. Position in the item's Files slice is the
// order, and the server guarantees it is returned in display order. The old
// model stored an int with `omitempty`, which made an explicit order of 0
// indistinguishable from no order at all - a whole class of bug avoided by
// not having the field.
type File struct {
	ID FileID `json:"id" dynamodbav:"id"`

	// Role is a slot id declared by the item's type - "disc", "case-front",
	// "page". Validated against the type on write, so a file can never land
	// in a slot its type never declared.
	Role string `json:"role" dynamodbav:"role"`

	Key         string `json:"key" dynamodbav:"key"`
	ContentType string `json:"contentType" dynamodbav:"contentType"`
	SizeBytes   int64  `json:"sizeBytes" dynamodbav:"sizeBytes"`

	// OriginalFilename is kept only for Content-Disposition on download. It
	// is deliberately NOT part of the S3 key: interpolating a caller-supplied
	// filename into a key invites traversal and silent collisions.
	OriginalFilename string `json:"originalFilename,omitempty" dynamodbav:"originalFilename,omitempty"`

	// Width and Height are filled in by the derivative pipeline. Until then
	// they are zero, which the client must tolerate - it is why every image
	// currently uses `fill` and gets no layout-shift protection.
	Width  int `json:"width,omitempty" dynamodbav:"width,omitempty"`
	Height int `json:"height,omitempty" dynamodbav:"height,omitempty"`

	Status     FileStatus `json:"status" dynamodbav:"status"`
	UploadedAt time.Time  `json:"uploadedAt" dynamodbav:"uploadedAt"`
}

// SubjectRef is a subject as denormalized onto an item: enough to render a
// gallery tile or a caption without a second lookup.
//
// People and pets share this shape deliberately. Kind distinguishes them, and
// the full record (a person's birth date, a pet's breed) lives on the subject
// itself. Keeping one array here rather than parallel people/animals arrays
// is what stops the filter grammar, the facets and the index projection
// multiplying with every new kind.
type SubjectRef struct {
	ID   SubjectID `json:"id" dynamodbav:"id"`
	Kind string    `json:"kind" dynamodbav:"kind"`
	Name string    `json:"name" dynamodbav:"name"`
}

// ItemCard is everything the gallery, the map and the timeline need, and
// exactly what the secondary index carries.
//
// The split from Item is not cosmetic: these are the attributes projected
// into GSI1, so one Query answers filtering, sorting, faceting and thumbnail
// rendering without touching the base table. Anything added here has to be
// added to the index projection too - and since DynamoDB projects only
// top-level attributes, a nested value like a date's sort key has to travel
// inside its parent map rather than on its own.
type ItemCard struct {
	ID        ItemID    `json:"id" dynamodbav:"-"`
	ArchiveID ArchiveID `json:"archiveId" dynamodbav:"-"`

	// TypeID and TypeVersion name the entry in the type registry this item
	// was created against. Storing the version means a later narrowing change
	// to that type can be detected rather than silently misapplied.
	TypeID      string `json:"typeId" dynamodbav:"typeId"`
	TypeVersion int    `json:"typeVersion" dynamodbav:"typeVersion"`

	Title string `json:"title" dynamodbav:"title"`

	// Date is exactly what the client sent; DateNorm is everything derived
	// from it. Keeping both means the wire shape never has to change.
	Date     *ArchiveDate `json:"date,omitempty" dynamodbav:"date,omitempty"`
	DateNorm *Normalized  `json:"dateNormalized,omitempty" dynamodbav:"dateNormalized,omitempty"`

	// Location is where this particular item was recorded - possibly a couple
	// of hundred metres from anything named. PlaceID is the optional link to
	// a canonical place, used for grouping. They answer different questions,
	// so an item carries both.
	Location *Location `json:"location,omitempty" dynamodbav:"location,omitempty"`
	PlaceID  PlaceID   `json:"placeId,omitempty" dynamodbav:"placeId,omitempty"`

	Tags     []string     `json:"tags" dynamodbav:"tags"`
	Subjects []SubjectRef `json:"subjects" dynamodbav:"subjects"`

	// Capabilities are the media families this item ACTUALLY holds, not what
	// its type permits - so "everything with audio" means items you can
	// really listen to, not every CD including the ones that are just
	// photographs of a disc.
	Capabilities []string `json:"capabilities" dynamodbav:"capabilities"`

	// The cover is denormalized flat rather than left inside Files, because
	// Files is not projected into the index. Without these, drawing a gallery
	// of sixty thumbnails would mean sixty base-table reads.
	CoverFileID FileID `json:"coverFileId,omitempty" dynamodbav:"coverFileId,omitempty"`

	// CoverPinned records that a person chose this cover, as opposed to the
	// server deriving it. Without the distinction a derived cover becomes
	// sticky: reorder a document's pages and the thumbnail keeps showing what
	// used to be page one, because a previously-derived id is
	// indistinguishable from a deliberate choice.
	CoverPinned bool `json:"coverPinned,omitempty" dynamodbav:"coverPinned,omitempty"`

	CoverKey         string `json:"-" dynamodbav:"coverKey,omitempty"`
	CoverContentType string `json:"-" dynamodbav:"coverContentType,omitempty"`
	CoverWidth       int    `json:"coverWidth,omitempty" dynamodbav:"coverW,omitempty"`
	CoverHeight      int    `json:"coverHeight,omitempty" dynamodbav:"coverH,omitempty"`

	FileCount int `json:"fileCount" dynamodbav:"fileCount"`

	CreatedAt time.Time `json:"createdAt" dynamodbav:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" dynamodbav:"updatedAt"`

	// Version backs the ETag and every conditional write. It is what turns
	// two people editing the same item from a silent lost update into a 412.
	Version int64 `json:"-" dynamodbav:"version"`
}

// Item is the full record: a card plus the parts too heavy to carry in an
// index. Embedding rather than duplicating means the card can never drift
// from the item it summarizes.
type Item struct {
	ItemCard

	Notes string `json:"notes,omitempty" dynamodbav:"notes,omitempty"`

	// Attributes holds the type-specific fields a manifest declares - a CD's
	// artist, a photo's nothing-at-all. Validated against the type; unknown
	// keys are rejected.
	Attributes map[string]any `json:"attributes,omitempty" dynamodbav:"attributes,omitempty"`

	Files []File `json:"files" dynamodbav:"files"`
}

// Type resolves this item's entry in the registry. Returns false for a type
// the running binary doesn't know, which is possible if an item was written
// by a newer deploy - callers should degrade rather than fail.
func (i *Item) Type(reg *itemtypes.Registry) (*itemtypes.Type, bool) {
	return reg.Get(i.TypeID)
}

// FilesInSlot returns the files filling one slot, in display order.
func (i *Item) FilesInSlot(role string) []File {
	var out []File
	for _, f := range i.Files {
		if f.Role == role {
			out = append(out, f)
		}
	}
	return out
}

// HasDate reports whether a real date was recorded, as opposed to the item
// merely having a position in the ordering. The timeline uses this to decide
// what to draw; the index sort key is not a reliable signal, because undated
// items are ordered at their creation time.
func (i *ItemCard) HasDate() bool { return i.DateNorm != nil }

// deriveCapabilities recomputes Capabilities from the files actually present.
func (i *Item) deriveCapabilities() {
	seen := map[itemtypes.MediaFamily]bool{}
	var out []string

	// Iterate the canonical family order rather than the file order, so the
	// stored value is stable and diffs cleanly.
	for _, fam := range itemtypes.AllFamilies() {
		for _, f := range i.Files {
			if seen[fam] {
				continue
			}
			if got, err := itemtypes.FamilyForContentType(f.ContentType); err == nil && got == fam {
				seen[fam] = true
				out = append(out, string(fam))
			}
		}
	}
	i.Capabilities = out
}

// deriveCover picks the cover from the type's preference order, unless one
// was explicitly chosen and still exists, then denormalizes enough of it to
// render a thumbnail from the index alone.
func (i *Item) deriveCover(reg *itemtypes.Registry) {
	chosen := i.findCover(reg)

	i.CoverFileID = ""
	i.CoverKey = ""
	i.CoverContentType = ""
	i.CoverWidth = 0
	i.CoverHeight = 0

	if chosen == nil {
		// Nothing renderable to point at; drop the pin too, so a cover whose
		// file was deleted does not keep a dangling id forever.
		i.CoverPinned = false
		return
	}
	i.CoverFileID = chosen.ID
	i.CoverKey = chosen.Key
	i.CoverContentType = chosen.ContentType
	i.CoverWidth = chosen.Width
	i.CoverHeight = chosen.Height
}

func (i *Item) findCover(reg *itemtypes.Registry) *File {
	// A deliberate choice wins, as long as it still points at a real file.
	// A derived one is recomputed every time, so it tracks the files.
	if i.CoverPinned && i.CoverFileID != "" {
		for idx := range i.Files {
			if i.Files[idx].ID == i.CoverFileID {
				return &i.Files[idx]
			}
		}
	}

	typ, ok := i.Type(reg)
	if !ok {
		return nil
	}

	for _, slot := range typ.Cover.Prefer {
		for idx := range i.Files {
			f := &i.Files[idx]
			if f.Role != slot {
				continue
			}
			// Only something renderable can be a cover; an audio file would
			// leave the gallery tile blank with no obvious cause.
			fam, err := itemtypes.FamilyForContentType(f.ContentType)
			if err != nil {
				continue
			}
			if fam == itemtypes.FamilyImage || fam == itemtypes.FamilyPDF {
				return f
			}
		}
	}
	return nil
}

// Normalize recomputes every derived field. The single place a write path
// must call before saving, so none of them can forget one.
func (i *Item) Normalize(reg *itemtypes.Registry) error {
	if i.Date != nil {
		i.Date.Resolve()
		n, err := i.Date.Normalize()
		if err != nil {
			return err
		}
		i.DateNorm = &n
	} else {
		i.DateNorm = nil
	}

	i.Location.Normalize()
	i.deriveCapabilities()
	i.deriveCover(reg)
	i.FileCount = len(i.Files)

	// Nil slices marshal to DynamoDB NULL and come back as nil, which then
	// serializes to JSON `null` where the client expects `[]`. Normalizing
	// here keeps every response shape honest.
	if i.Tags == nil {
		i.Tags = []string{}
	}
	if i.Subjects == nil {
		i.Subjects = []SubjectRef{}
	}
	if i.Files == nil {
		i.Files = []File{}
	}
	if i.Capabilities == nil {
		i.Capabilities = []string{}
	}
	return nil
}
