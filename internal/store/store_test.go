package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
)

// These run against DynamoDB Local (`make dev-up`). They are skipped when
// DYNAMODB_ENDPOINT is unset rather than silently passing against nothing -
// CI sets it, so a skip locally never hides a failure in CI.
//
// Each test gets its own table, so they can run in any order and leave
// nothing behind.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		t.Skip("DYNAMODB_ENDPOINT not set; run `make dev-up` and use `make test-integration`")
	}

	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("local", "local", ""),
		),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}

	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	table := "archive-test-" + uuid.NewString()
	createTestTable(t, ctx, client, table)

	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: &table})
	})

	return New(client, table, itemtypes.Default)
}

// createTestTable mirrors infra/terraform/dynamodb.tf. The two definitions
// are separate, so a change to the real schema has to be made here as well -
// and these tests are what will notice if it isn't.
func createTestTable(t *testing.T, ctx context.Context, client *dynamodb.Client, table string) {
	t.Helper()

	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   &table,
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
			IndexName: aws.String("gsi1"),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("gsi1pk"), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String("gsi1sk"), KeyType: types.KeyTypeRange},
			},
			// ALL rather than the production INCLUDE, so these tests exercise
			// ordering and paging without also encoding the projection list.
			// What the projection must contain is a Terraform concern.
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
	})
	if err != nil {
		t.Fatalf("create table %s: %v", table, err)
	}
}

func newPhoto(title string, date *domain.ArchiveDate) *domain.Item {
	return &domain.Item{
		ItemCard: domain.ItemCard{
			ID:          domain.NewItemID(),
			TypeID:      "photo",
			TypeVersion: 1,
			Title:       title,
			Date:        date,
		},
	}
}

func mustCreate(t *testing.T, s *Store, archive domain.ArchiveID, item *domain.Item) *domain.Item {
	t.Helper()
	item.ArchiveID = archive
	if err := s.CreateItem(context.Background(), item); err != nil {
		t.Fatalf("create item: %v", err)
	}
	return item
}

func TestCreateAndGetItem(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	created := mustCreate(t, s, archive, newPhoto("Blue Ridge", &domain.ArchiveDate{
		Kind: domain.DateExact, Date: "1952-06-15",
	}))

	got, err := s.GetItem(ctx, archive, created.ID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}

	if got.Title != "Blue Ridge" {
		t.Errorf("title = %q, want %q", got.Title, "Blue Ridge")
	}
	if got.ID != created.ID || got.ArchiveID != archive {
		t.Errorf("identity = %q/%q, want %q/%q", got.ArchiveID, got.ID, archive, created.ID)
	}
	// Derived on write, so the client never computes it.
	if got.DateNorm == nil || got.DateNorm.Sort == "" {
		t.Error("date was not normalized on write")
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
	// Empty collections must survive the round trip as empty, not null.
	if got.Files == nil || got.Tags == nil {
		t.Error("nil slices came back where empty ones were stored")
	}
}

// TestItemsAreScopedToTheirArchive is the security property that replaced the
// old "read the row, then compare accountId in Go" check.
func TestItemsAreScopedToTheirArchive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mine := domain.NewArchiveID()
	theirs := domain.NewArchiveID()

	item := mustCreate(t, s, mine, newPhoto("Private", nil))

	if _, err := s.GetItem(ctx, theirs, item.ID); err != ErrNotFound {
		t.Errorf("cross-archive get = %v, want ErrNotFound", err)
	}

	page, err := s.ListItems(ctx, theirs, ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("another archive's list returned %d items", len(page.Items))
	}

	// Mutating across archives must fail the same way, not silently no-op.
	if _, err := s.PatchItem(ctx, theirs, item.ID, domain.ItemPatch{
		Title: domain.SetTo("hijacked"),
	}, nil); err != ErrNotFound {
		t.Errorf("cross-archive patch = %v, want ErrNotFound", err)
	}
	if err := s.DeleteItem(ctx, theirs, item.ID, nil); err != ErrNotFound {
		t.Errorf("cross-archive delete = %v, want ErrNotFound", err)
	}

	// And the original is untouched.
	still, err := s.GetItem(ctx, mine, item.ID)
	if err != nil || still.Title != "Private" {
		t.Errorf("original item damaged: %v, %q", err, still.Title)
	}
}

func TestListIsOrderedAndPaginates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	years := []string{"1940", "1960", "1980", "2000", "2020"}
	for _, y := range years {
		mustCreate(t, s, archive, newPhoto("photo "+y, &domain.ArchiveDate{
			Kind: domain.DateExact, Date: y,
		}))
	}

	// Default is newest first, which is what the gallery and timeline expect.
	page, err := s.ListItems(ctx, archive, ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("page size = %d, want 2", len(page.Items))
	}
	if page.Items[0].Title != "photo 2020" || page.Items[1].Title != "photo 2000" {
		t.Fatalf("first page = %q, %q; want newest first", page.Items[0].Title, page.Items[1].Title)
	}
	if page.NextCursor == "" {
		t.Fatal("no cursor with more pages remaining")
	}

	// Walk the rest and check nothing is skipped or repeated.
	seen := []string{page.Items[0].Title, page.Items[1].Title}
	cursor := page.NextCursor
	for cursor != "" {
		page, err = s.ListItems(ctx, archive, ListOptions{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("list page: %v", err)
		}
		for _, item := range page.Items {
			seen = append(seen, item.Title)
		}
		cursor = page.NextCursor
	}

	if len(seen) != len(years) {
		t.Fatalf("paged through %d items, want %d (%v)", len(seen), len(years), seen)
	}
	want := []string{"photo 2020", "photo 2000", "photo 1980", "photo 1960", "photo 1940"}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("order = %v, want %v", seen, want)
		}
	}

	// Ascending is the same set in the opposite order.
	asc, err := s.ListItems(ctx, archive, ListOptions{Ascending: true, Limit: 10})
	if err != nil {
		t.Fatalf("list ascending: %v", err)
	}
	if asc.Items[0].Title != "photo 1940" {
		t.Errorf("ascending starts at %q, want photo 1940", asc.Items[0].Title)
	}
}

// TestCursorRejectsChangedFilters covers the silent-wrong-results case: a
// client changes a filter but reuses its cursor, and would otherwise be
// served a page computed against the old query.
func TestCursorRejectsChangedFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	for i := 0; i < 3; i++ {
		mustCreate(t, s, archive, newPhoto(fmt.Sprintf("photo %d", i), nil))
	}

	page, err := s.ListItems(ctx, archive, ListOptions{Limit: 1, FilterHash: "tag=beach"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("expected a cursor")
	}

	_, err = s.ListItems(ctx, archive, ListOptions{
		Limit: 1, Cursor: page.NextCursor, FilterHash: "tag=mountains",
	})
	if err != ErrBadCursor {
		t.Errorf("reused cursor under new filters = %v, want ErrBadCursor", err)
	}

	// The same filters still work.
	if _, err := s.ListItems(ctx, archive, ListOptions{
		Limit: 1, Cursor: page.NextCursor, FilterHash: "tag=beach",
	}); err != nil {
		t.Errorf("same-filter cursor rejected: %v", err)
	}
}

// TestConcurrentAppendsLoseNothing is the regression test for the documented
// lost-file bug: the old store read, appended in Go, and wrote back with no
// condition, so two uploads finishing together each wrote a list missing the
// other's file.
func TestConcurrentAppendsLoseNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	item := mustCreate(t, s, archive, newPhoto("Album page", nil))

	const uploads = 4
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)

	for i := 0; i < uploads; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.AppendFile(ctx, archive, item.ID, domain.File{
				Role:        "voice-memo",
				Key:         fmt.Sprintf("originals/%s/%d.m4a", item.ID, i),
				ContentType: "audio/mp4",
				SizeBytes:   1024,
			})
			if err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	got, err := s.GetItem(ctx, archive, item.ID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}

	// The property that matters is that no successful append vanished. A
	// write that lost its race reports the conflict rather than pretending.
	if len(got.Files) != succeeded {
		t.Errorf("item holds %d files but %d appends reported success", len(got.Files), succeeded)
	}
	if succeeded == 0 {
		t.Error("every concurrent append failed")
	}

	// File ids must be distinct, or "last attached wins" is back.
	seen := map[domain.FileID]bool{}
	for _, f := range got.Files {
		if seen[f.ID] {
			t.Errorf("duplicate file id %q", f.ID)
		}
		seen[f.ID] = true
	}
}

// TestIfMatchDetectsAStaleEdit covers optimistic concurrency: an edit based
// on a version someone else has already superseded is refused rather than
// silently overwriting their change.
func TestIfMatchDetectsAStaleEdit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	item := mustCreate(t, s, archive, newPhoto("Original", nil))
	stale := item.Version

	updated, err := s.PatchItem(ctx, archive, item.ID, domain.ItemPatch{
		Title: domain.SetTo("First edit"),
	}, &stale)
	if err != nil {
		t.Fatalf("first patch: %v", err)
	}
	if updated.Version != stale+1 {
		t.Errorf("version = %d, want %d", updated.Version, stale+1)
	}

	// Someone else now tries to save an edit based on the version they read
	// before that first change landed.
	_, err = s.PatchItem(ctx, archive, item.ID, domain.ItemPatch{
		Title: domain.SetTo("Conflicting edit"),
	}, &stale)
	if err != ErrVersionConflict {
		t.Fatalf("stale patch = %v, want ErrVersionConflict", err)
	}

	got, _ := s.GetItem(ctx, archive, item.ID)
	if got.Title != "First edit" {
		t.Errorf("title = %q; the stale edit overwrote the newer one", got.Title)
	}

	// Without a pinned version the same write simply succeeds - the caller
	// did not claim to be working from a particular version.
	if _, err := s.PatchItem(ctx, archive, item.ID, domain.ItemPatch{
		Title: domain.SetTo("Unpinned edit"),
	}, nil); err != nil {
		t.Errorf("unpinned patch: %v", err)
	}
}

// TestPatchClearsFields covers the state the old pointer-based patch could
// not express: removing a date rather than leaving it alone.
func TestPatchClearsFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	item := mustCreate(t, s, archive, newPhoto("Dated", &domain.ArchiveDate{
		Kind: domain.DateExact, Date: "1952",
	}))

	// Absent fields are left alone.
	got, err := s.PatchItem(ctx, archive, item.ID, domain.ItemPatch{
		Notes: domain.SetTo("a note"),
	}, nil)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if got.Date == nil {
		t.Fatal("an untouched date was cleared")
	}

	// An explicit clear removes it, and the derived fields follow.
	got, err = s.PatchItem(ctx, archive, item.ID, domain.ItemPatch{
		Date: domain.Clear[domain.ArchiveDate](),
	}, nil)
	if err != nil {
		t.Fatalf("clearing patch: %v", err)
	}
	if got.Date != nil || got.DateNorm != nil {
		t.Errorf("date = %v / %v after clear, want both nil", got.Date, got.DateNorm)
	}
	if got.Notes != "a note" {
		t.Errorf("notes = %q; clearing the date disturbed another field", got.Notes)
	}
}

func TestReorderAndDeleteFiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	item := mustCreate(t, s, archive, &domain.Item{
		ItemCard: domain.ItemCard{
			ID: domain.NewItemID(), TypeID: "document", TypeVersion: 1, Title: "Letter",
		},
	})

	for i := 0; i < 3; i++ {
		var err error
		item2, err := s.AppendFile(ctx, archive, item.ID, domain.File{
			Role:        "page",
			Key:         fmt.Sprintf("originals/%s/page-%d.jpg", item.ID, i),
			ContentType: "image/jpeg",
		})
		if err != nil {
			t.Fatalf("append page %d: %v", i, err)
		}
		item = &item2
	}

	if len(item.Files) != 3 {
		t.Fatalf("file count = %d, want 3", len(item.Files))
	}
	// The cover comes from the type's preference, so the gallery needs no
	// join and no client-side derivation.
	if item.CoverFileID != item.Files[0].ID || item.CoverKey == "" {
		t.Errorf("cover = %q/%q, want the first page", item.CoverFileID, item.CoverKey)
	}

	reversed := []domain.FileID{item.Files[2].ID, item.Files[1].ID, item.Files[0].ID}
	reordered, err := s.ReorderFiles(ctx, archive, item.ID, reversed, nil)
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	for i, want := range reversed {
		if reordered.Files[i].ID != want {
			t.Fatalf("order = %v, want %v", reordered.Files, reversed)
		}
	}
	// Order is position, so the cover follows the reorder.
	if reordered.CoverFileID != reversed[0] {
		t.Errorf("cover = %q, want %q after reorder", reordered.CoverFileID, reversed[0])
	}

	// A partial reorder is refused rather than quietly dropping files.
	if _, err := s.ReorderFiles(ctx, archive, item.ID, reversed[:2], nil); err == nil {
		t.Error("a reorder that omitted a file was accepted")
	}

	// A cover someone deliberately chose is not re-derived by later reorders.
	pinnedTo := reversed[2]
	pinned, err := s.PatchItem(ctx, archive, item.ID, domain.ItemPatch{
		CoverFileID: domain.SetTo(pinnedTo),
	}, nil)
	if err != nil {
		t.Fatalf("pin cover: %v", err)
	}
	if pinned.CoverFileID != pinnedTo || !pinned.CoverPinned {
		t.Fatalf("pinned cover = %q (pinned=%v), want %q", pinned.CoverFileID, pinned.CoverPinned, pinnedTo)
	}

	restored, err := s.ReorderFiles(ctx, archive, item.ID, []domain.FileID{
		reversed[1], reversed[2], reversed[0],
	}, nil)
	if err != nil {
		t.Fatalf("reorder after pinning: %v", err)
	}
	if restored.CoverFileID != pinnedTo {
		t.Errorf("cover = %q after reorder, want the pinned %q", restored.CoverFileID, pinnedTo)
	}

	// Clearing it hands the choice back to the server.
	unpinned, err := s.PatchItem(ctx, archive, item.ID, domain.ItemPatch{
		CoverFileID: domain.Clear[domain.FileID](),
	}, nil)
	if err != nil {
		t.Fatalf("unpin cover: %v", err)
	}
	if unpinned.CoverFileID != restored.Files[0].ID {
		t.Errorf("cover = %q after unpinning, want the first page %q",
			unpinned.CoverFileID, restored.Files[0].ID)
	}

	afterDelete, err := s.DeleteFile(ctx, archive, item.ID, unpinned.Files[0].ID, nil)
	if err != nil {
		t.Fatalf("delete file: %v", err)
	}
	if len(afterDelete.Files) != 2 {
		t.Errorf("file count = %d after delete, want 2", len(afterDelete.Files))
	}
	if afterDelete.CoverFileID == unpinned.CoverFileID {
		t.Error("cover still points at a deleted file")
	}
}

func TestDeleteItem(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	item := mustCreate(t, s, archive, newPhoto("Doomed", nil))

	if err := s.DeleteItem(ctx, archive, item.ID, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetItem(ctx, archive, item.ID); err != ErrNotFound {
		t.Errorf("get after delete = %v, want ErrNotFound", err)
	}
	if err := s.DeleteItem(ctx, archive, item.ID, nil); err != ErrNotFound {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

func TestCreateArchiveWritesBothDirections(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	owner := domain.AccountID("cognito-sub-" + uuid.NewString())

	archive, err := s.CreateArchive(ctx, "The Fortner Archive", owner, "John")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	got, err := s.GetArchive(ctx, archive.ID)
	if err != nil || got.Name != "The Fortner Archive" {
		t.Fatalf("get archive = %v, %q", err, got.Name)
	}

	// The membership row: what authorization reads on every request.
	member, err := s.GetMember(ctx, archive.ID, owner)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if member.Role != domain.RoleOwner {
		t.Errorf("role = %q, want owner", member.Role)
	}
	// The display name has to come from the frontend: this service only ever
	// sees a Cognito sub, never an email or a name.
	if member.DisplayName != "John" {
		t.Errorf("displayName = %q, want John", member.DisplayName)
	}

	// The mirror row: what answers "which archives am I in".
	memberships, err := s.ListArchivesForAccount(ctx, owner)
	if err != nil {
		t.Fatalf("list archives: %v", err)
	}
	if len(memberships) != 1 || memberships[0].ArchiveID != archive.ID {
		t.Fatalf("memberships = %v, want one pointing at %q", memberships, archive.ID)
	}

	// A non-member is not a member, and is told nothing more than that.
	if _, err := s.GetMember(ctx, archive.ID, "someone-else"); err != ErrNotFound {
		t.Errorf("non-member lookup = %v, want ErrNotFound", err)
	}
}

func TestMembershipChangesStayInSync(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	owner := domain.AccountID("owner-" + uuid.NewString())
	guest := domain.AccountID("guest-" + uuid.NewString())

	archive, err := s.CreateArchive(ctx, "Shared", owner, "Owner")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	if err := s.PutMember(ctx, domain.Member{
		ArchiveID: archive.ID, AccountID: guest,
		Role: domain.RoleViewer, DisplayName: "Guest",
	}); err != nil {
		t.Fatalf("put member: %v", err)
	}

	members, err := s.ListMembers(ctx, archive.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %v (%v), want 2", members, err)
	}

	// Promotion has to land on both rows, or the two directions disagree
	// about what someone is allowed to do.
	if err := s.PutMember(ctx, domain.Member{
		ArchiveID: archive.ID, AccountID: guest,
		Role: domain.RoleEditor, DisplayName: "Guest",
	}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	member, _ := s.GetMember(ctx, archive.ID, guest)
	if member.Role != domain.RoleEditor {
		t.Errorf("role = %q, want editor", member.Role)
	}
	mirrored, _ := s.ListArchivesForAccount(ctx, guest)
	if len(mirrored) != 1 || mirrored[0].Role != domain.RoleEditor {
		t.Errorf("mirror role = %v, want editor", mirrored)
	}

	if err := s.RemoveMember(ctx, archive.ID, guest); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if _, err := s.GetMember(ctx, archive.ID, guest); err != ErrNotFound {
		t.Errorf("removed member still present: %v", err)
	}
	if left, _ := s.ListArchivesForAccount(ctx, guest); len(left) != 0 {
		t.Errorf("mirror row survived removal: %v", left)
	}
}

func TestBumpArchiveVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	owner := domain.AccountID("owner-" + uuid.NewString())
	archive, err := s.CreateArchive(ctx, "Versioned", owner, "Owner")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	next, err := s.BumpArchiveVersion(ctx, archive.ID)
	if err != nil {
		t.Fatalf("bump: %v", err)
	}
	if next != archive.Version+1 {
		t.Errorf("version = %d, want %d", next, archive.Version+1)
	}

	if _, err := s.BumpArchiveVersion(ctx, domain.NewArchiveID()); err != ErrNotFound {
		t.Errorf("bump of a missing archive = %v, want ErrNotFound", err)
	}
}

// TestValidationRunsOnWrite confirms the type registry is enforced at the
// storage boundary, not only at the HTTP edge - so nothing can reach the
// table with a role its type never declared.
func TestValidationRunsOnWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	bad := newPhoto("Wrong type", nil)
	bad.TypeID = "not-a-real-type"
	bad.ArchiveID = archive
	if err := s.CreateItem(ctx, bad); err == nil {
		t.Error("an unknown item type was accepted")
	}

	item := mustCreate(t, s, archive, newPhoto("Real", nil))

	// "photo" declares no "disc" slot.
	if _, err := s.AppendFile(ctx, archive, item.ID, domain.File{
		Role: "disc", Key: "originals/x.jpg", ContentType: "image/jpeg",
	}); err == nil {
		t.Error("a file landed in a slot its type never declared")
	}

	// The photo slot holds exactly one file.
	if _, err := s.AppendFile(ctx, archive, item.ID, domain.File{
		Role: "photo", Key: "originals/a.jpg", ContentType: "image/jpeg",
	}); err != nil {
		t.Fatalf("first photo rejected: %v", err)
	}
	if _, err := s.AppendFile(ctx, archive, item.ID, domain.File{
		Role: "photo", Key: "originals/b.jpg", ContentType: "image/jpeg",
	}); err == nil {
		t.Error("a second file was accepted into a single-file slot")
	}

	// An audio file cannot go in an image slot.
	if _, err := s.AppendFile(ctx, archive, item.ID, domain.File{
		Role: "photo", Key: "originals/c.m4a", ContentType: "audio/mp4",
	}); err == nil {
		t.Error("audio was accepted into an image-only slot")
	}
}

func TestMain(m *testing.M) {
	// DynamoDB Local occasionally needs a moment after `make dev-up`.
	if os.Getenv("DYNAMODB_ENDPOINT") != "" {
		time.Sleep(50 * time.Millisecond)
	}
	os.Exit(m.Run())
}
