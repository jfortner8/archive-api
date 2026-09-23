package httpapi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/jfortner8/archive-api/internal/authtoken"
	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
	"github.com/jfortner8/archive-api/internal/store"
)

// fakeStore is an in-memory stand-in for the data layer.
//
// It re-implements archive scoping rather than ignoring it, because the
// scoping rules are exactly what these tests are checking: a fake that let
// every archive see every item would make the cross-archive tests pass
// without proving anything.
type fakeStore struct {
	mu sync.Mutex

	archives map[domain.ArchiveID]domain.Archive
	members  map[domain.ArchiveID]map[domain.AccountID]domain.Member
	items    map[domain.ArchiveID]map[domain.ItemID]domain.Item

	types *itemtypes.Registry

	// failNextWith, when set, is returned by the next store call. Used to
	// check how handlers map store errors onto responses.
	failNextWith error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		archives: map[domain.ArchiveID]domain.Archive{},
		members:  map[domain.ArchiveID]map[domain.AccountID]domain.Member{},
		items:    map[domain.ArchiveID]map[domain.ItemID]domain.Item{},
		types:    itemtypes.Default,
	}
}

func (f *fakeStore) take() error {
	err := f.failNextWith
	f.failNextWith = nil
	return err
}

func (f *fakeStore) seedArchive(id domain.ArchiveID, name string, owner domain.AccountID, role domain.Role) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.archives[id] = domain.Archive{ID: id, Name: name, CreatedBy: owner, Version: 1}
	if f.members[id] == nil {
		f.members[id] = map[domain.AccountID]domain.Member{}
	}
	f.members[id][owner] = domain.Member{ArchiveID: id, AccountID: owner, Role: role}
	if f.items[id] == nil {
		f.items[id] = map[domain.ItemID]domain.Item{}
	}
}

func (f *fakeStore) seedItem(archive domain.ArchiveID, item domain.Item) domain.Item {
	f.mu.Lock()
	defer f.mu.Unlock()

	item.ArchiveID = archive
	if item.ID == "" {
		item.ID = domain.NewItemID()
	}
	if item.Version == 0 {
		item.Version = 1
	}
	_ = item.Normalize(f.types)
	if f.items[archive] == nil {
		f.items[archive] = map[domain.ItemID]domain.Item{}
	}
	f.items[archive][item.ID] = item
	return item
}

func (f *fakeStore) CreateItem(_ context.Context, item *domain.Item) error {
	if err := f.take(); err != nil {
		return err
	}
	if err := item.Validate(f.types); err != nil {
		return err
	}
	if err := item.Normalize(f.types); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	item.Version = 1
	if f.items[item.ArchiveID] == nil {
		f.items[item.ArchiveID] = map[domain.ItemID]domain.Item{}
	}
	f.items[item.ArchiveID][item.ID] = *item
	return nil
}

func (f *fakeStore) GetItem(_ context.Context, archive domain.ArchiveID, id domain.ItemID) (domain.Item, error) {
	if err := f.take(); err != nil {
		return domain.Item{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	item, ok := f.items[archive][id]
	if !ok {
		return domain.Item{}, store.ErrNotFound
	}
	return item, nil
}

func (f *fakeStore) ListItems(_ context.Context, archive domain.ArchiveID, opts store.ListOptions) (store.ItemPage, error) {
	if err := f.take(); err != nil {
		return store.ItemPage{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	cards := make([]domain.ItemCard, 0, len(f.items[archive]))
	for _, item := range f.items[archive] {
		cards = append(cards, item.ItemCard)
	}
	sort.Slice(cards, func(i, j int) bool {
		if opts.Ascending {
			return cards[i].Title < cards[j].Title
		}
		return cards[i].Title > cards[j].Title
	})
	return store.ItemPage{Items: cards}, nil
}

func (f *fakeStore) PatchItem(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, patch domain.ItemPatch, ifVersion *int64) (domain.Item, error) {
	if err := f.take(); err != nil {
		return domain.Item{}, err
	}

	item, err := f.GetItem(ctx, archive, id)
	if err != nil {
		return domain.Item{}, err
	}
	if ifVersion != nil && *ifVersion != item.Version {
		return domain.Item{}, store.ErrVersionConflict
	}

	patch.Apply(&item)
	item.Version++
	if err := item.Validate(f.types); err != nil {
		return domain.Item{}, err
	}
	if err := item.Normalize(f.types); err != nil {
		return domain.Item{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[archive][id] = item
	return item, nil
}

func (f *fakeStore) DeleteItem(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, ifVersion *int64) error {
	if err := f.take(); err != nil {
		return err
	}
	item, err := f.GetItem(ctx, archive, id)
	if err != nil {
		return err
	}
	if ifVersion != nil && *ifVersion != item.Version {
		return store.ErrVersionConflict
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items[archive], id)
	return nil
}

func (f *fakeStore) AppendFile(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, file domain.File) (domain.Item, error) {
	if err := f.take(); err != nil {
		return domain.Item{}, err
	}
	item, err := f.GetItem(ctx, archive, id)
	if err != nil {
		return domain.Item{}, err
	}

	for _, existing := range item.Files {
		if existing.ID == file.ID {
			return item, nil
		}
	}
	item.Files = append(item.Files, file)
	item.Version++
	if err := item.Validate(f.types); err != nil {
		return domain.Item{}, err
	}
	if err := item.Normalize(f.types); err != nil {
		return domain.Item{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[archive][id] = item
	return item, nil
}

func (f *fakeStore) DeleteFile(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, fileID domain.FileID, ifVersion *int64) (domain.Item, error) {
	item, err := f.GetItem(ctx, archive, id)
	if err != nil {
		return domain.Item{}, err
	}
	if ifVersion != nil && *ifVersion != item.Version {
		return domain.Item{}, store.ErrVersionConflict
	}

	for idx, existing := range item.Files {
		if existing.ID == fileID {
			item.Files = append(item.Files[:idx:idx], item.Files[idx+1:]...)
			item.Version++
			_ = item.Normalize(f.types)

			f.mu.Lock()
			defer f.mu.Unlock()
			f.items[archive][id] = item
			return item, nil
		}
	}
	return domain.Item{}, store.ErrNotFound
}

func (f *fakeStore) ReorderFiles(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, order []domain.FileID, ifVersion *int64) (domain.Item, error) {
	item, err := f.GetItem(ctx, archive, id)
	if err != nil {
		return domain.Item{}, err
	}
	if ifVersion != nil && *ifVersion != item.Version {
		return domain.Item{}, store.ErrVersionConflict
	}
	if len(order) != len(item.Files) {
		return domain.Item{}, fmt.Errorf("reorder must list all %d files, got %d", len(item.Files), len(order))
	}

	byID := map[domain.FileID]domain.File{}
	for _, file := range item.Files {
		byID[file.ID] = file
	}
	reordered := make([]domain.File, 0, len(order))
	for _, fileID := range order {
		file, ok := byID[fileID]
		if !ok {
			return domain.Item{}, fmt.Errorf("reorder names unknown or duplicate file %q", fileID)
		}
		delete(byID, fileID)
		reordered = append(reordered, file)
	}

	item.Files = reordered
	item.Version++
	_ = item.Normalize(f.types)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[archive][id] = item
	return item, nil
}

func (f *fakeStore) CreateArchive(_ context.Context, name string, owner domain.AccountID, ownerName string) (domain.Archive, error) {
	if err := f.take(); err != nil {
		return domain.Archive{}, err
	}

	archive := domain.Archive{ID: domain.NewArchiveID(), Name: name, CreatedBy: owner, Version: 1}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.archives[archive.ID] = archive
	f.members[archive.ID] = map[domain.AccountID]domain.Member{
		owner: {ArchiveID: archive.ID, AccountID: owner, Role: domain.RoleOwner, DisplayName: ownerName},
	}
	f.items[archive.ID] = map[domain.ItemID]domain.Item{}
	return archive, nil
}

func (f *fakeStore) GetArchive(_ context.Context, id domain.ArchiveID) (domain.Archive, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	archive, ok := f.archives[id]
	if !ok {
		return domain.Archive{}, store.ErrNotFound
	}
	return archive, nil
}

func (f *fakeStore) GetMember(_ context.Context, archive domain.ArchiveID, account domain.AccountID) (domain.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	member, ok := f.members[archive][account]
	if !ok {
		return domain.Member{}, store.ErrNotFound
	}
	return member, nil
}

func (f *fakeStore) ListMembers(_ context.Context, archive domain.ArchiveID) ([]domain.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]domain.Member, 0, len(f.members[archive]))
	for _, member := range f.members[archive] {
		out = append(out, member)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}

func (f *fakeStore) ListArchivesForAccount(_ context.Context, account domain.AccountID) ([]domain.Member, error) {
	if err := f.take(); err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var out []domain.Member
	for archiveID, members := range f.members {
		if member, ok := members[account]; ok {
			member.ArchiveID = archiveID
			out = append(out, member)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ArchiveID < out[j].ArchiveID })
	return out, nil
}

func (f *fakeStore) PutMember(_ context.Context, member domain.Member) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.members[member.ArchiveID] == nil {
		f.members[member.ArchiveID] = map[domain.AccountID]domain.Member{}
	}
	f.members[member.ArchiveID][member.AccountID] = member
	return nil
}

func (f *fakeStore) RemoveMember(_ context.Context, archive domain.ArchiveID, account domain.AccountID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.members[archive], account)
	return nil
}

// fakePresigner returns a URL derived from the key, so tests can assert which
// object a URL points at without a real S3.
type fakePresigner struct{}

func (fakePresigner) PresignUpload(_ context.Context, key, _ string) (string, error) {
	return "https://s3.test/" + key + "?upload=1", nil
}

func (fakePresigner) PresignDownload(_ context.Context, key string) (string, error) {
	return "https://s3.test/" + key + "?download=1", nil
}

// fakeVerifier maps opaque tokens onto accounts.
type fakeVerifier map[string]domain.AccountID

func (f fakeVerifier) Verify(_ context.Context, token string) (authtoken.Claims, error) {
	account, ok := f[token]
	if !ok {
		return authtoken.Claims{}, authtoken.ErrInvalidToken
	}
	return authtoken.Claims{Sub: string(account)}, nil
}

func keyPrefix(key string) string {
	if idx := strings.LastIndexByte(key, '/'); idx >= 0 {
		return key[:idx]
	}
	return key
}
