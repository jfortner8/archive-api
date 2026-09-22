package api

import (
	"context"
	"fmt"

	"github.com/jfortner8/archive-api/internal/authtoken"
	"github.com/jfortner8/archive-api/internal/store"
)

// fakeItemsStore is an in-memory stand-in for *store.ItemStore, so handler
// tests don't need a real (or local) DynamoDB. Enforces accountID scoping
// the same way the real store does, so tests catch cross-account bugs,
// not just compile errors.
type fakeItemsStore struct {
	items map[string]store.Item
	err   error // when set, every call fails with this error
}

func newFakeItemsStore(seed ...store.Item) *fakeItemsStore {
	f := &fakeItemsStore{items: map[string]store.Item{}}
	for _, it := range seed {
		f.items[it.ID] = it
	}
	return f
}

func (f *fakeItemsStore) Create(_ context.Context, accountID, itemType, title string) (store.Item, error) {
	if f.err != nil {
		return store.Item{}, f.err
	}
	item := store.Item{
		ID:        fmt.Sprintf("item-%d", len(f.items)+1),
		AccountID: accountID,
		Type:      itemType,
		Title:     title,
		Files:     []store.File{},
	}
	f.items[item.ID] = item
	return item, nil
}

func (f *fakeItemsStore) Get(_ context.Context, id, accountID string) (store.Item, error) {
	if f.err != nil {
		return store.Item{}, f.err
	}
	item, ok := f.items[id]
	if !ok || item.AccountID != accountID {
		return store.Item{}, store.ErrNotFound
	}
	return item, nil
}

func (f *fakeItemsStore) List(_ context.Context, accountID string) ([]store.Item, error) {
	if f.err != nil {
		return nil, f.err
	}
	items := make([]store.Item, 0, len(f.items))
	for _, it := range f.items {
		if it.AccountID == accountID {
			items = append(items, it)
		}
	}
	return items, nil
}

func (f *fakeItemsStore) AddFile(_ context.Context, id, accountID string, file store.File) (store.Item, error) {
	if f.err != nil {
		return store.Item{}, f.err
	}
	item, ok := f.items[id]
	if !ok || item.AccountID != accountID {
		return store.Item{}, store.ErrNotFound
	}
	file.ID = fmt.Sprintf("file-%d", len(item.Files)+1)
	item.Files = append(item.Files, file)
	f.items[id] = item
	return item, nil
}

func (f *fakeItemsStore) Update(_ context.Context, id, accountID string, apply func(*store.Item)) (store.Item, error) {
	if f.err != nil {
		return store.Item{}, f.err
	}
	item, ok := f.items[id]
	if !ok || item.AccountID != accountID {
		return store.Item{}, store.ErrNotFound
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
