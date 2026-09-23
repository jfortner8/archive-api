package domain

import "github.com/google/uuid"

// Identifiers are distinct types rather than bare strings.
//
// This is cheap insurance for the one class of bug the storage layer is most
// exposed to. Keys are built by concatenating ids, and every id is a string,
// so passing an item id where an archive id belongs compiles perfectly and
// then writes a row nobody can ever find - or worse, one that belongs to the
// wrong archive. Distinct types make that a compile error instead.
type (
	// AccountID is a Cognito `sub`. It identifies a person who signs in, and
	// is never an archive: one account can belong to several archives.
	AccountID string

	// ArchiveID identifies a shared archive - the unit of ownership and the
	// partition key everything else hangs off.
	ArchiveID string

	ItemID    string
	FileID    string
	SubjectID string
	PlaceID   string
	RelID     string
)

// Ids carry a short human-readable prefix. It costs six bytes and repays them
// the first time you are staring at a raw DynamoDB row wondering what
// 4f3c2a1e-... is supposed to be.
const (
	prefixArchive = "arc_"
	prefixItem    = "itm_"
	prefixFile    = "fil_"
	prefixSubject = "sub_"
	prefixPlace   = "plc_"
	prefixRel     = "rel_"
)

func NewArchiveID() ArchiveID { return ArchiveID(prefixArchive + uuid.NewString()) }
func NewItemID() ItemID       { return ItemID(prefixItem + uuid.NewString()) }
func NewSubjectID() SubjectID { return SubjectID(prefixSubject + uuid.NewString()) }
func NewPlaceID() PlaceID     { return PlaceID(prefixPlace + uuid.NewString()) }
func NewRelID() RelID         { return RelID(prefixRel + uuid.NewString()) }

// NewFileID is minted before the upload rather than after it, so the S3 key
// can be built from it. That is what lets the key stop containing the
// caller's filename - and makes confirming an upload idempotent, since the
// client is confirming an id the server already chose.
func NewFileID() FileID { return FileID(prefixFile + uuid.NewString()) }
