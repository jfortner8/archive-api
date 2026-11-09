package store

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/store/dynamo"
)

// ListQuery is one page request against the items collection.
type ListQuery struct {
	Filter    domain.Filter
	Limit     int
	Cursor    string
	Ascending bool
}

// Spine reads every item card in an archive, in date order.
//
// One Query against the index, following its paging to the end. The index
// projects only common metadata, so a card is around 200 bytes and a few
// thousand items is a few hundred kilobytes - one or two round trips.
//
// Everything downstream filters this in memory rather than pushing predicates
// into DynamoDB, which is deliberate. Free text, multi-value tag matching,
// date-range overlap and bounding boxes are not key-addressable, and a
// FilterExpression would still read every row while costing the same. Doing
// it here means one code path serves the paginated list, the unpaginated
// spine and the facet counts, so they cannot disagree about what a filter
// means.
//
// This stops being the right shape somewhere past a few tens of thousands of
// items per archive, when the spine no longer fits comfortably in one
// invocation. The escape hatch is to cache it per warm container keyed on the
// archive's version counter, then to materialise it as a single S3 object -
// not to move the filtering into DynamoDB, which cannot express most of it.
func (s *Store) Spine(ctx context.Context, archive domain.ArchiveID, ascending bool) ([]domain.ItemCard, error) {
	var (
		cards    []domain.ItemCard
		startKey map[string]types.AttributeValue
	)

	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              &s.table,
			IndexName:              aws.String(dynamo.GSI1Name),
			KeyConditionExpression: aws.String("gsi1pk = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: dynamo.ItemIndexPK(archive)},
			},
			ScanIndexForward:  aws.Bool(ascending),
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("read spine: %w", err)
		}

		for _, av := range out.Items {
			card, err := unmarshalCard(av)
			if err != nil {
				return nil, err
			}
			cards = append(cards, card)
		}

		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}

	if cards == nil {
		cards = []domain.ItemCard{}
	}
	return cards, nil
}

// FilterSpine reads the spine and keeps what matches.
func (s *Store) FilterSpine(ctx context.Context, archive domain.ArchiveID, filter domain.Filter, ascending bool) ([]domain.ItemCard, error) {
	cards, err := s.Spine(ctx, archive, ascending)
	if err != nil {
		return nil, err
	}

	kept := make([]domain.ItemCard, 0, len(cards))
	for i := range cards {
		if filter.Matches(&cards[i]) {
			kept = append(kept, cards[i])
		}
	}
	return kept, nil
}

// ListItems returns one page of matching cards.
//
// Pagination is keyset rather than an offset: the cursor carries the index
// sort key of the last card returned, and the next page starts after it. An
// offset would shift under an insert, quietly repeating or skipping an item -
// and the client holds an index into this ordering to drive arrow-key
// navigation in the lightbox, so it has to be stable.
func (s *Store) ListItems(ctx context.Context, archive domain.ArchiveID, q ListQuery) (ItemPage, error) {
	matching, err := s.FilterSpine(ctx, archive, q.Filter, q.Ascending)
	if err != nil {
		return ItemPage{}, err
	}
	return Paginate(matching, q)
}

// Paginate cuts one page out of an already-filtered, already-ordered spine.
//
// Separate from ListItems, and exported, because this is the part with the
// subtle behaviour - cursor validation and the keyset seek - and keeping it a
// pure function means it can be tested over a handful of cards without a
// DynamoDB anywhere, and that test fakes page identically to production
// rather than approximating it.
func Paginate(matching []domain.ItemCard, q ListQuery) (ItemPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}

	after, err := decodeCursor(q.Cursor, q.Filter.Hash())
	if err != nil {
		return ItemPage{}, err
	}

	// Seek past everything at or before the cursor's key. Comparing keys
	// rather than counting rows is what makes this stable: an item inserted
	// ahead of the cursor shifts every offset but no key, so the next page
	// still begins exactly where the last one ended.
	start := 0
	if after != "" {
		for i := range matching {
			key := dynamo.ItemIndexSK(&matching[i])
			reached := key > after
			if !q.Ascending {
				reached = key < after
			}
			if reached {
				start = i
				break
			}
			start = i + 1
		}
	}

	end := start + limit
	if end > len(matching) {
		end = len(matching)
	}

	page := ItemPage{
		Items: matching[start:end],
		Total: len(matching),
	}
	if end < len(matching) {
		page.NextCursor, err = encodeCursor(q.Filter.Hash(), dynamo.ItemIndexSK(&matching[end-1]))
		if err != nil {
			return ItemPage{}, err
		}
	}
	if page.Items == nil {
		page.Items = []domain.ItemCard{}
	}
	return page, nil
}
