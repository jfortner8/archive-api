package api

import (
	"context"
	"fmt"

	"github.com/jfortner8/archive-api/internal/store"
)

// fakeItemsStore is an in-memory stand-in for *store.ItemStore, so handler
// tests don't need a real (or local) DynamoDB.
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

func (f *fakeItemsStore) Create(_ context.Context, itemType, title string) (store.Item, error) {
	if f.err != nil {
		return store.Item{}, f.err
	}
	item := store.Item{
		ID:    fmt.Sprintf("item-%d", len(f.items)+1),
		Type:  itemType,
		Title: title,
		Files: []store.File{},
	}
	f.items[item.ID] = item
	return item, nil
}

func (f *fakeItemsStore) Get(_ context.Context, id string) (store.Item, error) {
	if f.err != nil {
		return store.Item{}, f.err
	}
	item, ok := f.items[id]
	if !ok {
		return store.Item{}, store.ErrNotFound
	}
	return item, nil
}

func (f *fakeItemsStore) List(_ context.Context) ([]store.Item, error) {
	if f.err != nil {
		return nil, f.err
	}
	items := make([]store.Item, 0, len(f.items))
	for _, it := range f.items {
		items = append(items, it)
	}
	return items, nil
}

func (f *fakeItemsStore) AddFile(_ context.Context, id string, file store.File) (store.Item, error) {
	if f.err != nil {
		return store.Item{}, f.err
	}
	item, ok := f.items[id]
	if !ok {
		return store.Item{}, store.ErrNotFound
	}
	item.Files = append(item.Files, file)
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
