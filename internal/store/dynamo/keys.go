// Package dynamo owns the physical layout of the single DynamoDB table.
//
// Everything in an archive shares one partition key, so a single Query can
// fetch the archive and `begins_with(sk, ...)` selects an entity type:
//
//	ARCH#<aid>      / META                    the archive record
//	ARCH#<aid>      / MEMBER#<accountId>      membership + role
//	ACCT#<accountId>/ ARCH#<aid>              mirror: "which archives am I in"
//	ARCH#<aid>      / ITEM#<itemId>           an item, files embedded
//	ARCH#<aid>      / SUBJECT#<subjectId>     a person or a pet
//	ARCH#<aid>      / PLACE#<placeId>
//	ARCH#<aid>      / REL#<from>#<type>#<to>  a relationship edge
//
// This file is deliberately the only place that knows any of that. Key
// construction is where a mistake is both easiest to make and hardest to
// notice - a wrong archive id in a key does not error, it silently writes a
// row into someone else's archive - so it is kept in one small, heavily
// tested place rather than spread across the store methods.
package dynamo

import (
	"fmt"
	"strings"
	"time"

	"github.com/jfortner8/archive-api/internal/domain"
)

// Key is a primary key: partition plus sort.
type Key struct {
	PK string
	SK string
}

const (
	archivePartition = "ARCH#"
	accountPartition = "ACCT#"

	metaSK    = "META"
	memberSK  = "MEMBER#"
	itemSK    = "ITEM#"
	subjectSK = "SUBJECT#"
	placeSK   = "PLACE#"
	relSK     = "REL#"
)

// GSI1Name is the one secondary index: the archive's spine, ordered by date.
const GSI1Name = "gsi1"

func archivePK(id domain.ArchiveID) string { return archivePartition + string(id) }

// ArchiveKey addresses the archive record itself.
func ArchiveKey(id domain.ArchiveID) Key {
	return Key{PK: archivePK(id), SK: metaSK}
}

// MemberKey addresses one account's membership of one archive. This is the
// row the authorization middleware reads on every request, which is why it is
// a direct GetItem rather than anything requiring a query.
func MemberKey(archive domain.ArchiveID, account domain.AccountID) Key {
	return Key{PK: archivePK(archive), SK: memberSK + string(account)}
}

// MembershipMirrorKey addresses the same membership from the account's side,
// answering "which archives am I in".
//
// The membership is written twice, in one transaction, rather than adding a
// secondary index for the reverse lookup. Two writes on a rare operation
// (joining an archive) buys a strongly consistent read from either direction
// and one fewer index to pay for and keep consistent.
func MembershipMirrorKey(account domain.AccountID, archive domain.ArchiveID) Key {
	return Key{PK: accountPartition + string(account), SK: archivePartition + string(archive)}
}

// AccountPK is the partition holding every archive an account belongs to.
func AccountPK(account domain.AccountID) string {
	return accountPartition + string(account)
}

// ArchivePK is the partition holding everything in one archive.
func ArchivePK(archive domain.ArchiveID) string {
	return archivePK(archive)
}

func ItemKey(archive domain.ArchiveID, item domain.ItemID) Key {
	return Key{PK: archivePK(archive), SK: itemSK + string(item)}
}

func SubjectKey(archive domain.ArchiveID, subject domain.SubjectID) Key {
	return Key{PK: archivePK(archive), SK: subjectSK + string(subject)}
}

func PlaceKey(archive domain.ArchiveID, place domain.PlaceID) Key {
	return Key{PK: archivePK(archive), SK: placeSK + string(place)}
}

// RelKey addresses a relationship edge. The canonical direction is stored
// once and the inverse derived, so there is exactly one row per relationship
// and no way for the two directions to disagree.
func RelKey(archive domain.ArchiveID, from domain.SubjectID, relType string, to domain.SubjectID) Key {
	return Key{
		PK: archivePK(archive),
		SK: relSK + string(from) + "#" + relType + "#" + string(to),
	}
}

// Prefixes for begins_with queries over one archive's partition.
const (
	MemberPrefix  = memberSK
	ItemPrefix    = itemSK
	SubjectPrefix = subjectSK
	PlacePrefix   = placeSK
	RelPrefix     = relSK
)

// ItemIDFromSK recovers an item id from its sort key.
func ItemIDFromSK(sk string) (domain.ItemID, error) {
	id, ok := strings.CutPrefix(sk, itemSK)
	if !ok || id == "" {
		return "", fmt.Errorf("sort key %q is not an item", sk)
	}
	return domain.ItemID(id), nil
}

// ArchiveIDFromMirrorSK recovers an archive id from a membership mirror row.
func ArchiveIDFromMirrorSK(sk string) (domain.ArchiveID, error) {
	id, ok := strings.CutPrefix(sk, archivePartition)
	if !ok || id == "" {
		return "", fmt.Errorf("sort key %q is not an archive reference", sk)
	}
	return domain.ArchiveID(id), nil
}

// AccountIDFromMemberSK recovers an account id from a membership row.
func AccountIDFromMemberSK(sk string) (domain.AccountID, error) {
	id, ok := strings.CutPrefix(sk, memberSK)
	if !ok || id == "" {
		return "", fmt.Errorf("sort key %q is not a membership", sk)
	}
	return domain.AccountID(id), nil
}

// ItemIndexPK is GSI1's partition: every item in one archive.
func ItemIndexPK(archive domain.ArchiveID) string {
	return archivePK(archive) + "#ITEM"
}

// ItemIndexSK orders items by date, then by id to break ties into a total
// order - which matters because the client holds an index into this ordering
// and steps through it with the arrow keys. Two items sharing a date must not
// be able to swap places between requests.
//
// Undated items sort at their creation time rather than at a sentinel.
//
// The alternative - a sentinel that sorts last - only works in one direction:
// it would put undated items last ascending and FIRST descending, and
// descending is the default. Placing them at creation time needs no special
// case in either direction, and reads sensibly: an item whose date nobody has
// filled in yet appears where it was added. The index sort key is an ordering
// device, not a claim about when the photograph was taken - `dateNormalized`
// is absent for these items, and `hasDate=false` is what actually
// distinguishes them, which is also how the timeline excludes them.
func ItemIndexSK(item *domain.ItemCard) string {
	sortInstant := formatIndexInstant(item.CreatedAt)
	if item.DateNorm != nil && item.DateNorm.Sort != "" {
		sortInstant = item.DateNorm.Sort
	}
	return sortInstant + "#" + string(item.ID)
}

// formatIndexInstant matches the width and layout of a normalized date's sort
// value. The two are compared against each other inside one index, so a
// different width here would sort in the wrong place.
func formatIndexInstant(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// ArchiveIDFromPK recovers an archive id from a partition key.
func ArchiveIDFromPK(pk string) (domain.ArchiveID, error) {
	id, ok := strings.CutPrefix(pk, archivePartition)
	if !ok || id == "" {
		return "", fmt.Errorf("partition key %q is not an archive", pk)
	}
	return domain.ArchiveID(id), nil
}
