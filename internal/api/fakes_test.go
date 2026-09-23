package api

import (
	"context"
	"fmt"

	"github.com/jfortner8/archive-api/internal/authtoken"
	"github.com/jfortner8/archive-api/internal/legacystore"
)

// fakeItemsStore is an in-memory stand-in for *legacystore.ItemStore, so handler
// tests don't need a real (or local) DynamoDB. Enforces accountID scoping
// the same way the real store does, so tests catch cross-account bugs,
// not just compile errors.
type fakeItemsStore struct {
	items map[string]legacystore.Item
	err   error // when set, every call fails with this error
}

func newFakeItemsStore(seed ...legacystore.Item) *fakeItemsStore {
	f := &fakeItemsStore{items: map[string]legacystore.Item{}}
	for _, it := range seed {
		f.items[it.ID] = it
	}
	return f
}

func (f *fakeItemsStore) Create(_ context.Context, accountID, itemType, title string) (legacystore.Item, error) {
	if f.err != nil {
		return legacystore.Item{}, f.err
	}
	item := legacystore.Item{
		ID:        fmt.Sprintf("item-%d", len(f.items)+1),
		AccountID: accountID,
		Type:      itemType,
		Title:     title,
		Files:     []legacystore.File{},
	}
	f.items[item.ID] = item
	return item, nil
}

func (f *fakeItemsStore) Get(_ context.Context, id, accountID string) (legacystore.Item, error) {
	if f.err != nil {
		return legacystore.Item{}, f.err
	}
	item, ok := f.items[id]
	if !ok || item.AccountID != accountID {
		return legacystore.Item{}, legacystore.ErrNotFound
	}
	return item, nil
}

func (f *fakeItemsStore) List(_ context.Context, accountID string) ([]legacystore.Item, error) {
	if f.err != nil {
		return nil, f.err
	}
	items := make([]legacystore.Item, 0, len(f.items))
	for _, it := range f.items {
		if it.AccountID == accountID {
			items = append(items, it)
		}
	}
	return items, nil
}

func (f *fakeItemsStore) AddFile(_ context.Context, id, accountID string, file legacystore.File) (legacystore.Item, error) {
	if f.err != nil {
		return legacystore.Item{}, f.err
	}
	item, ok := f.items[id]
	if !ok || item.AccountID != accountID {
		return legacystore.Item{}, legacystore.ErrNotFound
	}
	file.ID = fmt.Sprintf("file-%d", len(item.Files)+1)
	item.Files = append(item.Files, file)
	f.items[id] = item
	return item, nil
}

func (f *fakeItemsStore) Update(_ context.Context, id, accountID string, apply func(*legacystore.Item)) (legacystore.Item, error) {
	if f.err != nil {
		return legacystore.Item{}, f.err
	}
	item, ok := f.items[id]
	if !ok || item.AccountID != accountID {
		return legacystore.Item{}, legacystore.ErrNotFound
	}
	apply(&item)
	f.items[id] = item
	return item, nil
}

// fakeFilesStore is an in-memory stand-in for *storage.FileStore.
type fakeFilesStore struct {
	err error
}

func (f *fakeFilesStore) PresignUpload(_ context.Context, key, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "https://example.test/upload/" + key, nil
}

func (f *fakeFilesStore) PresignDownload(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "https://example.test/download/" + key, nil
}

// fakeVerifier is an in-memory stand-in for *authtoken.CognitoVerifier, so
// handler tests don't need a real Cognito JWKS endpoint. Tokens are just
// opaque map keys, not real JWTs.
type fakeVerifier struct {
	tokens map[string]authtoken.Claims
	err    error // when set, every call fails with this error
}

func newFakeVerifier(tokens map[string]authtoken.Claims) *fakeVerifier {
	return &fakeVerifier{tokens: tokens}
}

func (f *fakeVerifier) Verify(_ context.Context, token string) (authtoken.Claims, error) {
	if f.err != nil {
		return authtoken.Claims{}, f.err
	}
	claims, ok := f.tokens[token]
	if !ok {
		return authtoken.Claims{}, authtoken.ErrInvalidToken
	}
	return claims, nil
}
