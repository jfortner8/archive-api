package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/store/dynamo"
)

// ListOptions controls a page of items.
type ListOptions struct {
	Limit  int
	Cursor string

	// FilterHash fingerprints the filters this page was produced under, and
	// is checked when the cursor comes back. Callers with no filters pass "".
	FilterHash string

	// Ascending walks oldest-first. The default is newest-first, matching the
	// timeline's own default.
	Ascending bool
}

// ItemPage is one page of cards plus the cursor for the next.
type ItemPage struct {
	Items      []domain.ItemCard
	NextCursor string
}

// CreateItem writes a new item, failing if one already exists at that id.
func (s *Store) CreateItem(ctx context.Context, item *domain.Item) error {
	now := time.Now().UTC()
	item.CreatedAt = now
	item.UpdatedAt = now
	item.Version = 1

	if err := item.Validate(s.types); err != nil {
		return err
	}
	if err := item.Normalize(s.types); err != nil {
		return err
	}

	av, err := marshalItem(item)
	if err != nil {
		return err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &s.table,
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create item: %w", err)
	}
	return nil
}

// GetItem fetches one item.
//
// Scoping is now part of the key rather than a comparison afterwards. The old
// implementation read the row by id alone and then compared accountId in Go,
// which meant another archive's data was in this process's memory before the
// decision to hide it was made.
func (s *Store) GetItem(ctx context.Context, archive domain.ArchiveID, id domain.ItemID) (domain.Item, error) {
	key := dynamo.ItemKey(archive, id)

	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table,
		Key:       keyToAV(key),
	})
	if err != nil {
		return domain.Item{}, fmt.Errorf("get item: %w", err)
	}
	if out.Item == nil {
		return domain.Item{}, ErrNotFound
	}
	return unmarshalItem(out.Item)
}

// ListItems returns a page of cards, newest first by default.
//
// This Queries the secondary index rather than scanning the table. The old
// implementation read every row in the table and discarded the ones belonging
// to other accounts - and, because it never passed on DynamoDB's
// LastEvaluatedKey, silently stopped at the first megabyte. Past a few hundred
// items, photographs simply went missing with no error anywhere.
func (s *Store) ListItems(ctx context.Context, archive domain.ArchiveID, opts ListOptions) (ItemPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}

	startKey, err := decodeCursor(opts.Cursor, opts.FilterHash)
	if err != nil {
		return ItemPage{}, err
	}

	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		IndexName:              aws.String(dynamo.GSI1Name),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: dynamo.ItemIndexPK(archive)},
		},
		ScanIndexForward:  aws.Bool(opts.Ascending),
		Limit:             aws.Int32(int32(limit)),
		ExclusiveStartKey: startKey,
	})
	if err != nil {
		return ItemPage{}, fmt.Errorf("list items: %w", err)
	}

	page := ItemPage{Items: make([]domain.ItemCard, 0, len(out.Items))}
	for _, av := range out.Items {
		card, err := unmarshalCard(av)
		if err != nil {
			return ItemPage{}, err
		}
		page.Items = append(page.Items, card)
	}

	page.NextCursor, err = encodeCursor(opts.FilterHash, out.LastEvaluatedKey)
	if err != nil {
		return ItemPage{}, err
	}
	return page, nil
}

// PatchItem applies a partial metadata update.
//
// ifVersion, when supplied, is the version the caller last saw - an If-Match.
// If the item has moved on, the write is refused with ErrVersionConflict so
// the caller learns their edit was based on stale data. When it is omitted,
// a conflict is retried internally instead.
func (s *Store) PatchItem(
	ctx context.Context,
	archive domain.ArchiveID,
	id domain.ItemID,
	patch domain.ItemPatch,
	ifVersion *int64,
) (domain.Item, error) {
	return s.mutateItem(ctx, archive, id, ifVersion, func(item *domain.Item) error {
		patch.Apply(item)
		return nil
	})
}

// DeleteItem removes an item. The caller is responsible for sweeping its S3
// objects afterwards - in that order, because an orphaned object is invisible
// and cheap to clean up later, while an orphaned row is a broken image in the
// gallery.
func (s *Store) DeleteItem(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, ifVersion *int64) error {
	input := &dynamodb.DeleteItemInput{
		TableName:           &s.table,
		Key:                 keyToAV(dynamo.ItemKey(archive, id)),
		ConditionExpression: aws.String("attribute_exists(pk)"),
	}

	if ifVersion != nil {
		input.ConditionExpression = aws.String("attribute_exists(pk) AND version = :v")
		input.ExpressionAttributeValues = map[string]types.AttributeValue{
			":v": numberAV(*ifVersion),
		}
	}

	_, err := s.client.DeleteItem(ctx, input)
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			if ifVersion != nil {
				// Either it is gone or it moved on; the caller's precondition
				// failed either way.
				return ErrVersionConflict
			}
			return ErrNotFound
		}
		return fmt.Errorf("delete item: %w", err)
	}
	return nil
}

// AppendFile attaches a file to an item.
//
// The old implementation read the item, appended in Go, and wrote the whole
// thing back with no condition - so two uploads finishing at once would each
// write a list missing the other's file, and one upload just vanished. Here
// the write is conditional on the version that was read, so a lost update is
// impossible; an unpinned caller simply retries.
func (s *Store) AppendFile(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, file domain.File) (domain.Item, error) {
	if file.ID == "" {
		file.ID = domain.NewFileID()
	}
	if file.UploadedAt.IsZero() {
		file.UploadedAt = time.Now().UTC()
	}
	if file.Status == "" {
		file.Status = domain.FileStatusPending
	}

	return s.mutateItem(ctx, archive, id, nil, func(item *domain.Item) error {
		for _, existing := range item.Files {
			if existing.ID == file.ID {
				// Confirming the same upload twice is a retry, not a second
				// file. Minting the id before the upload is what makes this
				// safely idempotent.
				return nil
			}
		}
		item.Files = append(item.Files, file)
		return nil
	})
}

// DeleteFile detaches a file.
func (s *Store) DeleteFile(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, fileID domain.FileID, ifVersion *int64) (domain.Item, error) {
	return s.mutateItem(ctx, archive, id, ifVersion, func(item *domain.Item) error {
		for idx, f := range item.Files {
			if f.ID == fileID {
				item.Files = append(item.Files[:idx], item.Files[idx+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}

// ReorderFiles sets the display order by replacing the whole list.
//
// Whole-list replacement rather than per-file index arithmetic: the order is
// simply the order of the slice, so there is nothing to renumber and no way
// for two files to claim the same position.
func (s *Store) ReorderFiles(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, order []domain.FileID, ifVersion *int64) (domain.Item, error) {
	return s.mutateItem(ctx, archive, id, ifVersion, func(item *domain.Item) error {
		if len(order) != len(item.Files) {
			return fmt.Errorf("reorder must list all %d files, got %d", len(item.Files), len(order))
		}

		byID := make(map[domain.FileID]domain.File, len(item.Files))
		for _, f := range item.Files {
			byID[f.ID] = f
		}

		reordered := make([]domain.File, 0, len(order))
		for _, fileID := range order {
			f, ok := byID[fileID]
			if !ok {
				return fmt.Errorf("reorder names unknown or duplicate file %q", fileID)
			}
			delete(byID, fileID)
			reordered = append(reordered, f)
		}

		item.Files = reordered
		return nil
	})
}

// mutateItem is read, modify, conditional write, with a bounded retry.
//
// This is deliberately not an UpdateItem expression. Several stored fields are
// derived from the item as a whole - the cover, the capabilities, the index
// sort key - and none of them can be recomputed from a partial patch without
// seeing the rest of the item. Reading first is what makes them correct, and
// the version condition is what makes it safe: the old code's bug was the
// missing condition, not the read.
func (s *Store) mutateItem(
	ctx context.Context,
	archive domain.ArchiveID,
	id domain.ItemID,
	ifVersion *int64,
	mutate func(*domain.Item) error,
) (domain.Item, error) {
	for attempt := 0; ; attempt++ {
		item, err := s.GetItem(ctx, archive, id)
		if err != nil {
			return domain.Item{}, err
		}

		if ifVersion != nil && *ifVersion != item.Version {
			return domain.Item{}, ErrVersionConflict
		}

		expected := item.Version

		if err := mutate(&item); err != nil {
			return domain.Item{}, err
		}

		item.UpdatedAt = time.Now().UTC()
		item.Version = expected + 1

		if err := item.Validate(s.types); err != nil {
			return domain.Item{}, err
		}
		if err := item.Normalize(s.types); err != nil {
			return domain.Item{}, err
		}

		err = s.putItemIfVersion(ctx, &item, expected)
		if err == nil {
			return item, nil
		}
		if !errors.Is(err, ErrVersionConflict) {
			return domain.Item{}, err
		}

		// The caller pinned a version, so a conflict is genuinely their
		// answer: someone else edited it and they should be told, not have
		// their stale edit quietly reapplied.
		if ifVersion != nil || attempt >= maxConflictRetries {
			return domain.Item{}, ErrVersionConflict
		}
	}
}

func (s *Store) putItemIfVersion(ctx context.Context, item *domain.Item, expected int64) error {
	av, err := marshalItem(item)
	if err != nil {
		return err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &s.table,
		Item:                av,
		ConditionExpression: aws.String("version = :expected"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":expected": numberAV(expected),
		},
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return ErrVersionConflict
		}
		return fmt.Errorf("put item: %w", err)
	}
	return nil
}

func marshalItem(item *domain.Item) (map[string]types.AttributeValue, error) {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return nil, fmt.Errorf("marshal item: %w", err)
	}

	key := dynamo.ItemKey(item.ArchiveID, item.ID)
	av["pk"] = &types.AttributeValueMemberS{Value: key.PK}
	av["sk"] = &types.AttributeValueMemberS{Value: key.SK}
	av["gsi1pk"] = &types.AttributeValueMemberS{Value: dynamo.ItemIndexPK(item.ArchiveID)}
	av["gsi1sk"] = &types.AttributeValueMemberS{Value: dynamo.ItemIndexSK(&item.ItemCard)}
	return av, nil
}

func unmarshalItem(av map[string]types.AttributeValue) (domain.Item, error) {
	var item domain.Item
	if err := attributevalue.UnmarshalMap(av, &item); err != nil {
		return domain.Item{}, fmt.Errorf("unmarshal item: %w", err)
	}
	if err := fillIdentity(&item.ItemCard, av); err != nil {
		return domain.Item{}, err
	}
	return item, nil
}

func unmarshalCard(av map[string]types.AttributeValue) (domain.ItemCard, error) {
	var card domain.ItemCard
	if err := attributevalue.UnmarshalMap(av, &card); err != nil {
		return domain.ItemCard{}, fmt.Errorf("unmarshal item card: %w", err)
	}
	if err := fillIdentity(&card, av); err != nil {
		return domain.ItemCard{}, err
	}
	return card, nil
}

// fillIdentity recovers the ids from the keys. They are stored only in pk/sk
// rather than duplicated as attributes, so there is exactly one source of
// truth for where a row lives.
func fillIdentity(card *domain.ItemCard, av map[string]types.AttributeValue) error {
	pk, err := stringAttr(av, "pk")
	if err != nil {
		return err
	}
	sk, err := stringAttr(av, "sk")
	if err != nil {
		return err
	}

	archiveID, err := dynamo.ArchiveIDFromPK(pk)
	if err != nil {
		return err
	}
	itemID, err := dynamo.ItemIDFromSK(sk)
	if err != nil {
		return err
	}

	card.ArchiveID = archiveID
	card.ID = itemID
	return nil
}
