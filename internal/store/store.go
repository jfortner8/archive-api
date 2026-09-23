// Package store reads and writes the archive's data in DynamoDB.
//
// It speaks domain types and hides the table layout entirely: the physical
// keys live in the dynamo subpackage, and nothing above this package knows
// what a partition key is.
package store

import (
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
)

var (
	// ErrNotFound covers both "does not exist" and "exists but you are not a
	// member of its archive". Callers must not distinguish the two - telling
	// someone an id exists but is not theirs is itself a disclosure.
	ErrNotFound = errors.New("not found")

	// ErrVersionConflict means a conditional write lost a race. It surfaces
	// as a 412 when the caller supplied If-Match, and is retried internally
	// when they did not.
	ErrVersionConflict = errors.New("version conflict")

	// ErrAlreadyExists guards creates against clobbering an existing row.
	ErrAlreadyExists = errors.New("already exists")

	// ErrBadCursor means a pagination cursor was malformed, or belonged to a
	// different set of filters. Rejecting it is deliberate - silently serving
	// a page from the wrong query returns wrong data with no error.
	ErrBadCursor = errors.New("invalid cursor")
)

// Store is the data layer. One instance is shared by every request.
type Store struct {
	client *dynamodb.Client
	table  string

	// types is consulted when normalizing an item, to resolve cover
	// preference and validate slots.
	types *itemtypes.Registry
}

func New(client *dynamodb.Client, table string, types *itemtypes.Registry) *Store {
	return &Store{client: client, table: table, types: types}
}

// Types exposes the registry the store was built with, so callers that
// already hold a Store need not be handed the registry separately.
func (s *Store) Types() *itemtypes.Registry { return s.types }

// maxConflictRetries bounds the automatic retry on an unpinned conditional
// write. Two people editing the same item at the same moment is rare, and
// three attempts covers it; more would just be masking a hot-spot.
const maxConflictRetries = 3

// defaultPageLimit and maxPageLimit bound a list request.
const (
	defaultPageLimit = 60
	maxPageLimit     = 200
)
