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

// CreateArchive writes the archive and its first membership together.
//
// All three rows - the archive, the owner's membership, and the mirror that
// answers "which archives am I in" - go in one transaction. Half-created
// state here is the worst kind: an archive with no members belongs to nobody
// and cannot be deleted through the API, because deleting it requires being
// its owner.
func (s *Store) CreateArchive(ctx context.Context, name string, owner domain.AccountID, ownerName string) (domain.Archive, error) {
	now := time.Now().UTC()

	archive := domain.Archive{
		ID:        domain.NewArchiveID(),
		Name:      name,
		CreatedBy: owner,
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}

	member := domain.Member{
		ArchiveID:   archive.ID,
		AccountID:   owner,
		Role:        domain.RoleOwner,
		DisplayName: ownerName,
		JoinedAt:    now,
	}

	archiveAV, err := marshalWithKey(&archive, dynamo.ArchiveKey(archive.ID))
	if err != nil {
		return domain.Archive{}, err
	}
	memberAV, err := marshalWithKey(&member, dynamo.MemberKey(archive.ID, owner))
	if err != nil {
		return domain.Archive{}, err
	}
	mirrorAV, err := marshalWithKey(&member, dynamo.MembershipMirrorKey(owner, archive.ID))
	if err != nil {
		return domain.Archive{}, err
	}

	_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{
				TableName:           &s.table,
				Item:                archiveAV,
				ConditionExpression: aws.String("attribute_not_exists(pk)"),
			}},
			{Put: &types.Put{TableName: &s.table, Item: memberAV}},
			{Put: &types.Put{TableName: &s.table, Item: mirrorAV}},
		},
	})
	if err != nil {
		return domain.Archive{}, fmt.Errorf("create archive: %w", err)
	}
	return archive, nil
}

// GetArchive fetches the archive record.
func (s *Store) GetArchive(ctx context.Context, id domain.ArchiveID) (domain.Archive, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table,
		Key:       keyToAV(dynamo.ArchiveKey(id)),
	})
	if err != nil {
		return domain.Archive{}, fmt.Errorf("get archive: %w", err)
	}
	if out.Item == nil {
		return domain.Archive{}, ErrNotFound
	}

	var archive domain.Archive
	if err := attributevalue.UnmarshalMap(out.Item, &archive); err != nil {
		return domain.Archive{}, fmt.Errorf("unmarshal archive: %w", err)
	}
	archive.ID = id
	return archive, nil
}

// GetMember returns an account's membership of an archive, or ErrNotFound if
// there isn't one.
//
// This is the authorization check, so it runs on essentially every request.
// It is a direct GetItem by key rather than a query precisely for that
// reason: one round trip, strongly consistent, no index to lag behind.
func (s *Store) GetMember(ctx context.Context, archive domain.ArchiveID, account domain.AccountID) (domain.Member, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &s.table,
		Key:            keyToAV(dynamo.MemberKey(archive, account)),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return domain.Member{}, fmt.Errorf("get member: %w", err)
	}
	if out.Item == nil {
		return domain.Member{}, ErrNotFound
	}

	var member domain.Member
	if err := attributevalue.UnmarshalMap(out.Item, &member); err != nil {
		return domain.Member{}, fmt.Errorf("unmarshal member: %w", err)
	}
	member.ArchiveID = archive
	member.AccountID = account
	return member, nil
}

// ListMembers returns everyone in an archive.
func (s *Store) ListMembers(ctx context.Context, archive domain.ArchiveID) ([]domain.Member, error) {
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: dynamo.ArchivePK(archive)},
			":prefix": &types.AttributeValueMemberS{Value: dynamo.MemberPrefix},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}

	members := make([]domain.Member, 0, len(out.Items))
	for _, av := range out.Items {
		var member domain.Member
		if err := attributevalue.UnmarshalMap(av, &member); err != nil {
			return nil, fmt.Errorf("unmarshal member: %w", err)
		}
		sk, err := stringAttr(av, "sk")
		if err != nil {
			return nil, err
		}
		account, err := dynamo.AccountIDFromMemberSK(sk)
		if err != nil {
			return nil, err
		}
		member.ArchiveID = archive
		member.AccountID = account
		members = append(members, member)
	}
	return members, nil
}

// ListArchivesForAccount answers "which archives am I in", reading the mirror
// rows rather than a secondary index.
func (s *Store) ListArchivesForAccount(ctx context.Context, account domain.AccountID) ([]domain.Member, error) {
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.table,
		KeyConditionExpression: aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: dynamo.AccountPK(account)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list archives for account: %w", err)
	}

	memberships := make([]domain.Member, 0, len(out.Items))
	for _, av := range out.Items {
		var member domain.Member
		if err := attributevalue.UnmarshalMap(av, &member); err != nil {
			return nil, fmt.Errorf("unmarshal membership: %w", err)
		}
		sk, err := stringAttr(av, "sk")
		if err != nil {
			return nil, err
		}
		archiveID, err := dynamo.ArchiveIDFromMirrorSK(sk)
		if err != nil {
			return nil, err
		}
		member.ArchiveID = archiveID
		member.AccountID = account
		memberships = append(memberships, member)
	}
	return memberships, nil
}

// PutMember adds or updates a membership, writing both directions together so
// the two can never disagree about someone's role.
func (s *Store) PutMember(ctx context.Context, member domain.Member) error {
	if !member.Role.Valid() {
		return fmt.Errorf("unknown role %q", member.Role)
	}
	if member.JoinedAt.IsZero() {
		member.JoinedAt = time.Now().UTC()
	}

	memberAV, err := marshalWithKey(&member, dynamo.MemberKey(member.ArchiveID, member.AccountID))
	if err != nil {
		return err
	}
	mirrorAV, err := marshalWithKey(&member, dynamo.MembershipMirrorKey(member.AccountID, member.ArchiveID))
	if err != nil {
		return err
	}

	_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: &s.table, Item: memberAV}},
			{Put: &types.Put{TableName: &s.table, Item: mirrorAV}},
		},
	})
	if err != nil {
		return fmt.Errorf("put member: %w", err)
	}
	return nil
}

// RemoveMember revokes a membership from both directions at once.
func (s *Store) RemoveMember(ctx context.Context, archive domain.ArchiveID, account domain.AccountID) error {
	_, err := s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Delete: &types.Delete{TableName: &s.table, Key: keyToAV(dynamo.MemberKey(archive, account))}},
			{Delete: &types.Delete{TableName: &s.table, Key: keyToAV(dynamo.MembershipMirrorKey(account, archive))}},
		},
	})
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}

// BumpArchiveVersion increments the archive's version counter and returns the
// new value.
//
// Any write anywhere in the archive bumps this. It is what makes a collection
// or spine response cheaply cacheable: hash the filters together with this
// number for an ETag, and a client holding the current version gets a 304
// with no body instead of the entire archive again. That matters because the
// timeline and the map both fetch the whole spine on every mount.
func (s *Store) BumpArchiveVersion(ctx context.Context, archive domain.ArchiveID) (int64, error) {
	out, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           &s.table,
		Key:                 keyToAV(dynamo.ArchiveKey(archive)),
		UpdateExpression:    aws.String("SET version = version + :one, updatedAt = :now"),
		ConditionExpression: aws.String("attribute_exists(pk)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":one": numberAV(1),
			":now": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339Nano)},
		},
		ReturnValues: types.ReturnValueUpdatedNew,
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("bump archive version: %w", err)
	}

	var updated struct {
		Version int64 `dynamodbav:"version"`
	}
	if err := attributevalue.UnmarshalMap(out.Attributes, &updated); err != nil {
		return 0, fmt.Errorf("unmarshal archive version: %w", err)
	}
	return updated.Version, nil
}

// marshalWithKey marshals a record and stamps its primary key on.
func marshalWithKey(v any, key dynamo.Key) (map[string]types.AttributeValue, error) {
	av, err := attributevalue.MarshalMap(v)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	av["pk"] = &types.AttributeValueMemberS{Value: key.PK}
	av["sk"] = &types.AttributeValueMemberS{Value: key.SK}
	return av, nil
}
