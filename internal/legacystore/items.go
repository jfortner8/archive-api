// Package legacystore is the original item store, kept verbatim so the
// existing endpoints keep behaving exactly as they did while the replacement
// in internal/store is built alongside it.
//
// It is deleted, together with internal/api, when the new HTTP surface lands.
// Nothing new should be added here.
package legacystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

// ErrNotFound is returned when an item doesn't exist.
var ErrNotFound = errors.New("item not found")

// ArchiveDate is when something happened, which isn't always a single
// known day - Kind selects which of the other fields apply:
//   - "exact": Date is the day
//   - "range": Start/End bound it
//   - "circa": Date is the center point, Unit how wide the uncertainty is
//     (e.g. "decade")
type ArchiveDate struct {
	Kind  string `dynamodbav:"kind" json:"kind"`
	Date  string `dynamodbav:"date,omitempty" json:"date,omitempty"`
	Start string `dynamodbav:"start,omitempty" json:"start,omitempty"`
	End   string `dynamodbav:"end,omitempty" json:"end,omitempty"`
	Unit  string `dynamodbav:"unit,omitempty" json:"unit,omitempty"`
}

// ArchiveLocation is where something happened. Lat/Lon/Confidence are
// optional - a location can be just a text label with no coordinates.
type ArchiveLocation struct {
	Label      string   `dynamodbav:"label" json:"label"`
	Lat        *float64 `dynamodbav:"lat,omitempty" json:"lat,omitempty"`
	Lon        *float64 `dynamodbav:"lon,omitempty" json:"lon,omitempty"`
	Confidence string   `dynamodbav:"confidence,omitempty" json:"confidence,omitempty"`
}

// File describes one stored blob (a photo, a PDF page, a voice memo, ...)
// attached to an item. ID is what individual files are addressed by
// (e.g. for downloads) - Role alone isn't unique, since an item can have
// several files sharing a role (multiple voice memos, multi-page
// documents), unlike the front/back case it was originally designed for.
type File struct {
	ID          string `dynamodbav:"id" json:"id"`
	Role        string `dynamodbav:"role" json:"role"`                       // e.g. "front", "back", "page", "voice-memo"
	Order       int    `dynamodbav:"order,omitempty" json:"order,omitempty"` // page/sequence order within its role
	Key         string `dynamodbav:"key" json:"key"`                         // S3 object key
	ContentType string `dynamodbav:"contentType" json:"contentType"`
	SizeBytes   int64  `dynamodbav:"sizeBytes" json:"sizeBytes"`
}

// Item is one archived record: metadata plus zero or more attached files.
// Date/Location/Notes/Tags/People are all optional at creation and set
// later via Update - a capture flow often knows the file before it knows
// the details.
type Item struct {
	ID        string           `dynamodbav:"id" json:"id"`
	AccountID string           `dynamodbav:"accountId" json:"accountId"` // whose catalog this belongs to - today always a Cognito user's sub, but named for a future org/team account too
	Type      string           `dynamodbav:"type" json:"type"`           // e.g. "photo", "document" - open-ended for future data types
	Title     string           `dynamodbav:"title" json:"title"`
	Date      *ArchiveDate     `dynamodbav:"date,omitempty" json:"date,omitempty"`
	Location  *ArchiveLocation `dynamodbav:"location,omitempty" json:"location,omitempty"`
	Notes     string           `dynamodbav:"notes,omitempty" json:"notes,omitempty"`
	Tags      []string         `dynamodbav:"tags,omitempty" json:"tags,omitempty"`
	People    []string         `dynamodbav:"people,omitempty" json:"people,omitempty"`
	Files     []File           `dynamodbav:"files" json:"files"`
	CreatedAt time.Time        `dynamodbav:"createdAt" json:"createdAt"`
	UpdatedAt time.Time        `dynamodbav:"updatedAt" json:"updatedAt"`
}

// ItemStore reads and writes Items in a single DynamoDB table.
type ItemStore struct {
	client    *dynamodb.Client
	tableName string
}

func NewItemStore(client *dynamodb.Client, tableName string) *ItemStore {
	return &ItemStore{client: client, tableName: tableName}
}

// Create writes a new item with a generated ID and returns it.
func (s *ItemStore) Create(ctx context.Context, accountID, itemType, title string) (Item, error) {
	now := time.Now().UTC()
	item := Item{
		ID:        uuid.NewString(),
		AccountID: accountID,
		Type:      itemType,
		Title:     title,
		Files:     []File{},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.put(ctx, item); err != nil {
		return Item{}, err
	}
	return item, nil
}

// Get fetches a single item by ID, scoped to accountID. An item that
// exists but belongs to a different account is reported as ErrNotFound,
// same as one that doesn't exist at all - callers should never be able to
// tell the two apart.
func (s *ItemStore) Get(ctx context.Context, id, accountID string) (Item, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.tableName,
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
	})
	if err != nil {
		return Item{}, fmt.Errorf("get item: %w", err)
	}
	if out.Item == nil {
		return Item{}, ErrNotFound
	}

	var item Item
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return Item{}, fmt.Errorf("unmarshal item: %w", err)
	}
	if item.AccountID != accountID {
		return Item{}, ErrNotFound
	}
	return item, nil
}

// List returns every item belonging to accountID. Fine at hobby scale to
// Scan the whole table and filter in Go; switch to a Query against a
// GSI on accountID if the table grows large.
func (s *ItemStore) List(ctx context.Context, accountID string) ([]Item, error) {
	out, err := s.client.Scan(ctx, &dynamodb.ScanInput{
		TableName: &s.tableName,
	})
	if err != nil {
		return nil, fmt.Errorf("scan items: %w", err)
	}

	all := make([]Item, 0, len(out.Items))
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &all); err != nil {
		return nil, fmt.Errorf("unmarshal items: %w", err)
	}

	items := make([]Item, 0, len(all))
	for _, item := range all {
		if item.AccountID == accountID {
			items = append(items, item)
		}
	}
	return items, nil
}

// AddFile attaches a file to an existing item, assigning it a generated ID.
//
// This is a read-modify-write, not an atomic append: two concurrent
// uploads to the same item could race and overwrite each other's file
// list. Fine for a single-user hobby project; switch to an UpdateItem
// with a list_append expression if that ever stops being true.
func (s *ItemStore) AddFile(ctx context.Context, id, accountID string, file File) (Item, error) {
	item, err := s.Get(ctx, id, accountID)
	if err != nil {
		return Item{}, err
	}

	file.ID = uuid.NewString()
	item.Files = append(item.Files, file)
	item.UpdatedAt = time.Now().UTC()

	if err := s.put(ctx, item); err != nil {
		return Item{}, err
	}
	return item, nil
}

// Update applies apply to an existing item's metadata and saves the
// result. apply should only touch the fields it means to change - Update
// takes care of stamping UpdatedAt.
//
// Same read-modify-write caveat as AddFile: not safe under concurrent
// writes to the same item, which is fine at single-user hobby scale.
func (s *ItemStore) Update(ctx context.Context, id, accountID string, apply func(*Item)) (Item, error) {
	item, err := s.Get(ctx, id, accountID)
	if err != nil {
		return Item{}, err
	}

	apply(&item)
	item.UpdatedAt = time.Now().UTC()

	if err := s.put(ctx, item); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (s *ItemStore) put(ctx context.Context, item Item) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal item: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.tableName,
		Item:      av,
	})
	if err != nil {
		return fmt.Errorf("put item: %w", err)
	}
	return nil
}
