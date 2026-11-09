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

// CreateSubject writes a new person, pet, or whatever kind comes next.
func (s *Store) CreateSubject(ctx context.Context, subject *domain.Subject) error {
	now := time.Now().UTC()
	subject.CreatedAt = now
	subject.UpdatedAt = now
	subject.Version = 1

	if err := subject.Validate(s.subjectKinds); err != nil {
		return err
	}
	if err := subject.Normalize(s.subjectKinds); err != nil {
		return err
	}
	s.resolveSubjectCover(ctx, subject)

	av, err := marshalWithKey(subject, dynamo.SubjectKey(subject.ArchiveID, subject.ID))
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
		return fmt.Errorf("create subject: %w", err)
	}
	return nil
}

func (s *Store) GetSubject(ctx context.Context, archive domain.ArchiveID, id domain.SubjectID) (domain.Subject, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table,
		Key:       keyToAV(dynamo.SubjectKey(archive, id)),
	})
	if err != nil {
		return domain.Subject{}, fmt.Errorf("get subject: %w", err)
	}
	if out.Item == nil {
		return domain.Subject{}, ErrNotFound
	}

	var subject domain.Subject
	if err := attributevalue.UnmarshalMap(out.Item, &subject); err != nil {
		return domain.Subject{}, fmt.Errorf("unmarshal subject: %w", err)
	}
	subject.ID = id
	subject.ArchiveID = archive
	return subject, nil
}

// ListSubjects returns every subject in an archive, optionally of one kind.
//
// Unpaginated on purpose. An archive has tens or hundreds of people and
// pets, not thousands, and every surface that uses this - a People tab, a
// tag picker, a family tree - needs the whole set at once. Paginating it
// would make all three of them harder for no benefit.
func (s *Store) ListSubjects(ctx context.Context, archive domain.ArchiveID, kind string) ([]domain.Subject, error) {
	var subjects []domain.Subject
	var startKey map[string]types.AttributeValue

	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              &s.table,
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":     &types.AttributeValueMemberS{Value: dynamo.ArchivePK(archive)},
				":prefix": &types.AttributeValueMemberS{Value: dynamo.SubjectPrefix},
			},
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("list subjects: %w", err)
		}

		for _, av := range out.Items {
			subject, err := unmarshalSubject(av, archive)
			if err != nil {
				return nil, err
			}
			if kind != "" && subject.Kind != kind {
				continue
			}
			subjects = append(subjects, subject)
		}

		// Paging is followed here rather than ignored, which is the bug the
		// old item list had: without this a large archive silently returns
		// only its first megabyte.
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}

	if subjects == nil {
		subjects = []domain.Subject{}
	}
	return subjects, nil
}

func (s *Store) PatchSubject(
	ctx context.Context,
	archive domain.ArchiveID,
	id domain.SubjectID,
	patch domain.SubjectPatch,
	ifVersion *int64,
) (domain.Subject, error) {
	for attempt := 0; ; attempt++ {
		subject, err := s.GetSubject(ctx, archive, id)
		if err != nil {
			return domain.Subject{}, err
		}
		if ifVersion != nil && *ifVersion != subject.Version {
			return domain.Subject{}, ErrVersionConflict
		}

		expected := subject.Version
		patch.Apply(&subject)
		subject.UpdatedAt = time.Now().UTC()
		subject.Version = expected + 1

		if err := subject.Validate(s.subjectKinds); err != nil {
			return domain.Subject{}, err
		}
		if err := subject.Normalize(s.subjectKinds); err != nil {
			return domain.Subject{}, err
		}
		s.resolveSubjectCover(ctx, &subject)

		av, err := marshalWithKey(&subject, dynamo.SubjectKey(archive, id))
		if err != nil {
			return domain.Subject{}, err
		}

		_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:           &s.table,
			Item:                av,
			ConditionExpression: aws.String("version = :expected"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":expected": numberAV(expected),
			},
		})
		if err == nil {
			return subject, nil
		}

		var failed *types.ConditionalCheckFailedException
		if !errors.As(err, &failed) {
			return domain.Subject{}, fmt.Errorf("patch subject: %w", err)
		}
		if ifVersion != nil || attempt >= maxConflictRetries {
			return domain.Subject{}, ErrVersionConflict
		}
	}
}

// DeleteSubject removes a subject and every edge touching it.
//
// Leaving the edges would strand references to something that no longer
// exists, and a tree walk would then have to defend against missing nodes on
// every hop. Cleaning up here keeps the graph an invariant rather than
// something every reader re-checks.
func (s *Store) DeleteSubject(ctx context.Context, archive domain.ArchiveID, id domain.SubjectID, ifVersion *int64) error {
	relationships, err := s.ListRelationships(ctx, archive)
	if err != nil {
		return err
	}

	var writes []types.TransactWriteItem
	for _, rel := range relationships {
		if rel.FromID != id && rel.ToID != id {
			continue
		}
		writes = append(writes, types.TransactWriteItem{Delete: &types.Delete{
			TableName: &s.table,
			Key:       keyToAV(dynamo.RelKey(archive, rel.FromID, string(rel.Type), rel.ToID)),
		}})
	}

	subjectDelete := &types.Delete{
		TableName:           &s.table,
		Key:                 keyToAV(dynamo.SubjectKey(archive, id)),
		ConditionExpression: aws.String("attribute_exists(pk)"),
	}
	if ifVersion != nil {
		subjectDelete.ConditionExpression = aws.String("attribute_exists(pk) AND version = :v")
		subjectDelete.ExpressionAttributeValues = map[string]types.AttributeValue{
			":v": numberAV(*ifVersion),
		}
	}
	writes = append(writes, types.TransactWriteItem{Delete: subjectDelete})

	// A transaction caps out at 100 writes. Somebody with that many
	// relationships is remarkable enough to be worth an explicit error
	// rather than a partial delete.
	if len(writes) > 100 {
		return fmt.Errorf("subject has too many relationships to delete in one transaction (%d)", len(writes)-1)
	}

	_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes})
	if err != nil {
		var cancelled *types.TransactionCanceledException
		if errors.As(err, &cancelled) {
			if ifVersion != nil {
				return ErrVersionConflict
			}
			return ErrNotFound
		}
		return fmt.Errorf("delete subject: %w", err)
	}
	return nil
}

// PutRelationship writes one edge, after checking it against the vocabulary
// and against the graph it is joining.
func (s *Store) PutRelationship(ctx context.Context, rel *domain.Relationship) error {
	from, err := s.GetSubject(ctx, rel.ArchiveID, rel.FromID)
	if err != nil {
		return err
	}
	to, err := s.GetSubject(ctx, rel.ArchiveID, rel.ToID)
	if err != nil {
		return err
	}

	if err := domain.ValidateRelationship(rel, &from, &to); err != nil {
		return err
	}

	// The cycle check needs the existing graph, so it happens here rather
	// than in pure validation. One mistaken edge is all it takes to make
	// someone their own ancestor, after which rendering loops and the cause
	// is invisible edge by edge.
	if rel.Type == domain.RelParent {
		graph, err := s.LoadGraph(ctx, rel.ArchiveID)
		if err != nil {
			return err
		}
		if graph.WouldCycle(rel.FromID, rel.ToID) {
			return domain.ValidationErrors{{
				Field: "fromSubjectId", Code: "cycle",
				Message: fmt.Sprintf("%q is already a descendant of %q, so this would make someone their own ancestor",
					from.DisplayName, to.DisplayName),
			}}
		}
	}

	now := time.Now().UTC()
	rel.ID = domain.RelIDFor(rel.FromID, rel.Type, rel.ToID)
	if rel.CreatedAt.IsZero() {
		rel.CreatedAt = now
	}
	rel.UpdatedAt = now
	rel.Version = 1

	av, err := marshalWithKey(rel, dynamo.RelKey(rel.ArchiveID, rel.FromID, string(rel.Type), rel.ToID))
	if err != nil {
		return err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.table, Item: av})
	if err != nil {
		return fmt.Errorf("put relationship: %w", err)
	}
	return nil
}

func (s *Store) DeleteRelationship(ctx context.Context, archive domain.ArchiveID, id domain.RelID) error {
	from, relType, to, err := domain.ParseRelID(id)
	if err != nil {
		return ErrNotFound
	}

	_, err = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:           &s.table,
		Key:                 keyToAV(dynamo.RelKey(archive, from, string(relType), to)),
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return ErrNotFound
		}
		return fmt.Errorf("delete relationship: %w", err)
	}
	return nil
}

// ListRelationships returns every edge in an archive.
func (s *Store) ListRelationships(ctx context.Context, archive domain.ArchiveID) ([]domain.Relationship, error) {
	var relationships []domain.Relationship
	var startKey map[string]types.AttributeValue

	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              &s.table,
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":     &types.AttributeValueMemberS{Value: dynamo.ArchivePK(archive)},
				":prefix": &types.AttributeValueMemberS{Value: dynamo.RelPrefix},
			},
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("list relationships: %w", err)
		}

		for _, av := range out.Items {
			var rel domain.Relationship
			if err := attributevalue.UnmarshalMap(av, &rel); err != nil {
				return nil, fmt.Errorf("unmarshal relationship: %w", err)
			}
			rel.ArchiveID = archive
			rel.ID = domain.RelIDFor(rel.FromID, rel.Type, rel.ToID)
			relationships = append(relationships, rel)
		}

		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}

	if relationships == nil {
		relationships = []domain.Relationship{}
	}
	return relationships, nil
}

// LoadGraph reads an archive's subjects and edges and builds the adjacency
// needed to walk them.
//
// Two queries, then everything in memory. A graph feature usually implies a
// graph database; here it does not, because every entity in an archive shares
// one partition and a few hundred subjects with their edges is a few hundred
// kilobytes. Reading the whole edge set is also why edges are stored once in
// their canonical direction - traversal in either direction costs nothing, so
// there are no inverse rows to drift apart.
//
// This stops being the right approach somewhere past a few thousand
// subjects, when the edges no longer fit one query page.
func (s *Store) LoadGraph(ctx context.Context, archive domain.ArchiveID) (*domain.Graph, error) {
	subjects, err := s.ListSubjects(ctx, archive, "")
	if err != nil {
		return nil, err
	}
	relationships, err := s.ListRelationships(ctx, archive)
	if err != nil {
		return nil, err
	}

	subjectPtrs := make([]*domain.Subject, 0, len(subjects))
	for i := range subjects {
		subjectPtrs = append(subjectPtrs, &subjects[i])
	}
	relPtrs := make([]*domain.Relationship, 0, len(relationships))
	for i := range relationships {
		relPtrs = append(relPtrs, &relationships[i])
	}

	return domain.NewGraph(subjectPtrs, relPtrs), nil
}

func unmarshalSubject(av map[string]types.AttributeValue, archive domain.ArchiveID) (domain.Subject, error) {
	var subject domain.Subject
	if err := attributevalue.UnmarshalMap(av, &subject); err != nil {
		return domain.Subject{}, fmt.Errorf("unmarshal subject: %w", err)
	}

	sk, err := stringAttr(av, "sk")
	if err != nil {
		return domain.Subject{}, err
	}
	id, err := dynamo.SubjectIDFromSK(sk)
	if err != nil {
		return domain.Subject{}, err
	}

	subject.ID = id
	subject.ArchiveID = archive
	return subject, nil
}

// resolveSubjectCover copies the portrait item's object key onto the subject.
//
// Denormalized at write time for the same reason an item carries its own
// cover key: a family tree of sixty faces should be one response, not sixty
// lookups. A portrait pointing at an item that has since been deleted simply
// leaves the subject without a picture rather than failing the write.
func (s *Store) resolveSubjectCover(ctx context.Context, subject *domain.Subject) {
	subject.CoverKey = ""
	if subject.CoverItemID == "" {
		return
	}
	if item, err := s.GetItem(ctx, subject.ArchiveID, subject.CoverItemID); err == nil {
		subject.CoverKey = item.CoverKey
	}
}
