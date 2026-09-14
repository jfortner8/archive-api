// Package store handles reading and writing item metadata in DynamoDB.
package store

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

// File describes one stored blob (a photo, a PDF, ...) attached to an item.
type File struct {
	Role        string `dynamodbav:"role" json:"role"` // e.g. "front", "back", "primary"
	Key         string `dynamodbav:"key" json:"key"`   // S3 object key
	ContentType string `dynamodbav:"contentType" json:"contentType"`
	SizeBytes   int64  `dynamodbav:"sizeBytes" json:"sizeBytes"`
}

// Item is one archived record: metadata plus zero or more attached files.
type Item struct {
	ID        string    `dynamodbav:"id" json:"id"`
	Type      string    `dynamodbav:"type" json:"type"` // e.g. "photo", "document" - open-ended for future data types
	Title     string    `dynamodbav:"title" json:"title"`
	Files     []File    `dynamodbav:"files" json:"files"`
	CreatedAt time.Time `dynamodbav:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `dynamodbav:"updatedAt" json:"updatedAt"`
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
func (s *ItemStore) Create(ctx context.Context, itemType, title string) (Item, error) {
	now := time.Now().UTC()
	item := Item{
		ID:        uuid.NewString(),
		Type:      itemType,
		Title:     title,
		Files:     []File{},
		CreatedAt: now,
		UpdatedAt: now,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return Item{}, fmt.Errorf("marshal item: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.tableName,
		Item:      av,
	})
	if err != nil {
		return Item{}, fmt.Errorf("put item: %w", err)
	}

	return item, nil
}

// Get fetches a single item by ID.
func (s *ItemStore) Get(ctx context.Context, id string) (Item, error) {
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
	return item, nil
}

// List returns every item. Fine at hobby scale; switch to a paginated
// query (e.g. by type, via a GSI) if the table grows large.
func (s *ItemStore) List(ctx context.Context) ([]Item, error) {
	out, err := s.client.Scan(ctx, &dynamodb.ScanInput{
		TableName: &s.tableName,
	})
	if err != nil {
		return nil, fmt.Errorf("scan items: %w", err)
	}

	items := make([]Item, 0, len(out.Items))
	if err := attributevalue.UnmarshalListOfMaps(out.Items, &items); err != nil {
		return nil, fmt.Errorf("unmarshal items: %w", err)
	}
	return items, nil
}

// AddFile attaches a file to an existing item.
//
// This is a read-modify-write, not an atomic append: two concurrent
// uploads to the same item could race and overwrite each other's file
// list. Fine for a single-user hobby project; switch to an UpdateItem
// with a list_append expression if that ever stops being true.
func (s *ItemStore) AddFile(ctx context.Context, id string, file File) (Item, error) {
	item, err := s.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}

	item.Files = append(item.Files, file)
	item.UpdatedAt = time.Now().UTC()

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return Item{}, fmt.Errorf("marshal item: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.tableName,
		Item:      av,
	})
	if err != nil {
		return Item{}, fmt.Errorf("put item: %w", err)
	}

	return item, nil
}
