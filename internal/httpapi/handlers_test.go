package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
)

func itemsPath() string { return fmt.Sprintf("/v1/archives/%s/items", testArchive) }
func itemPath(id string) string {
	return fmt.Sprintf("/v1/archives/%s/items/%s", testArchive, id)
}

func TestCreateAndGetItem(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "POST", itemsPath(), requestOpts{
		token: ownerToken,
		body: map[string]any{
			"typeId": "photo",
			"title":  "Blue Ridge",
			"date":   map[string]any{"kind": "exact", "date": "1952-06-15"},
			"tags":   []string{"mountains"},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag on the created item")
	}

	created := decode[gen.Item](t, rec)
	if created.Title != "Blue Ridge" {
		t.Errorf("title = %q", created.Title)
	}
	// Derived server-side, so no client ever parses a date.
	if created.DateNormalized == nil {
		t.Error("no dateNormalized on the response")
	}
	// Empty collections are [] rather than null, so clients need no
	// nil-guards on every list.
	if created.Tags == nil || created.Files == nil || created.Subjects == nil {
		t.Error("a collection came back null where it should be empty")
	}

	got := do(t, s, "GET", itemPath(created.Id), requestOpts{token: ownerToken})
	if got.Code != http.StatusOK {
		t.Fatalf("get: status = %d", got.Code)
	}
}

// TestDownloadURLsAreInline is the regression test for the change that
// mattered most to the frontend: a gallery used to need one presign request
// per file per item, all of them awaited before the first pixel rendered.
func TestDownloadURLsAreInline(t *testing.T) {
	s, data := newTestServer(t)

	item := data.seedItem(testArchive, domain.Item{
		ItemCard: domain.ItemCard{TypeID: "photo", TypeVersion: 1, Title: "With files"},
		Files: []domain.File{
			{ID: "fil_a", Role: "photo", Key: "originals/a.jpg", ContentType: "image/jpeg", Status: domain.FileStatusReady},
			{ID: "fil_b", Role: "voice-memo", Key: "originals/b.m4a", ContentType: "audio/mp4", Status: domain.FileStatusReady},
		},
	})

	t.Run("on the item", func(t *testing.T) {
		rec := do(t, s, "GET", itemPath(string(item.ID)), requestOpts{token: ownerToken})
		got := decode[gen.Item](t, rec)

		if len(got.Files) != 2 {
			t.Fatalf("file count = %d, want 2", len(got.Files))
		}
		for _, f := range got.Files {
			if f.Url == nil || *f.Url == "" {
				t.Errorf("file %s has no inline url", f.Id)
				continue
			}
			if !strings.Contains(*f.Url, "download=1") {
				t.Errorf("file %s url = %q, not a download url", f.Id, *f.Url)
			}
			if f.UrlExpiresIn == nil || *f.UrlExpiresIn <= 0 {
				t.Errorf("file %s has no url expiry", f.Id)
			}
		}
	})

	t.Run("on the list", func(t *testing.T) {
		rec := do(t, s, "GET", itemsPath(), requestOpts{token: ownerToken})
		page := decode[struct {
			Data []gen.ItemCard `json:"data"`
			Page gen.PageInfo   `json:"page"`
		}](t, rec)

		if len(page.Data) != 1 {
			t.Fatalf("card count = %d, want 1", len(page.Data))
		}
		card := page.Data[0]
		if card.CoverUrl == nil || *card.CoverUrl == "" {
			t.Fatal("card has no cover url; the gallery would need a second request per tile")
		}
		// The cover is the photo, not the voice memo - an audio file would
		// leave the tile blank.
		if !strings.Contains(*card.CoverUrl, "a.jpg") {
			t.Errorf("cover url = %q, want the image", *card.CoverUrl)
		}
	})
}

func TestETagAndIfMatch(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "POST", itemsPath(), requestOpts{
		token: ownerToken,
		body:  map[string]any{"typeId": "photo", "title": "Original"},
	})
	created := decode[gen.Item](t, rec)
	etag := rec.Header().Get("ETag")

	// An edit based on the version we read succeeds.
	ok := do(t, s, "PATCH", itemPath(created.Id), requestOpts{
		token: ownerToken, ifMatch: etag,
		body: map[string]any{"title": "First edit"},
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("patch with a current ETag: status = %d, body = %s", ok.Code, ok.Body.String())
	}
	if ok.Header().Get("ETag") == etag {
		t.Error("the ETag did not change after an edit")
	}

	// The same edit replayed with the now-stale ETag is refused, rather than
	// silently overwriting the change that landed in between.
	stale := do(t, s, "PATCH", itemPath(created.Id), requestOpts{
		token: ownerToken, ifMatch: etag,
		body: map[string]any{"title": "Conflicting edit"},
	})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("patch with a stale ETag: status = %d, want 412", stale.Code)
	}
	if body := decode[gen.Error](t, stale); body.Error.Code != gen.ErrorCodePreconditionFailed {
		t.Errorf("code = %q, want precondition_failed", body.Error.Code)
	}

	// A garbled precondition is rejected rather than ignored - treating it as
	// "no precondition" would defeat the point of sending one.
	garbled := do(t, s, "PATCH", itemPath(created.Id), requestOpts{
		token: ownerToken, ifMatch: `"not-a-version"`,
		body: map[string]any{"title": "x"},
	})
	if garbled.Code != http.StatusBadRequest {
		t.Errorf("patch with a malformed If-Match: status = %d, want 400", garbled.Code)
	}
}

// TestPatchCanClearAField covers the state the previous API could not
// express at all: removing a date rather than leaving it alone.
func TestPatchCanClearAField(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "POST", itemsPath(), requestOpts{
		token: ownerToken,
		body: map[string]any{
			"typeId": "photo", "title": "Dated",
			"date":  map[string]any{"kind": "exact", "date": "1952"},
			"notes": "a note",
		},
	})
	created := decode[gen.Item](t, rec)

	// Absent means "leave alone".
	untouched := decode[gen.Item](t, do(t, s, "PATCH", itemPath(created.Id), requestOpts{
		token: ownerToken, body: map[string]any{"notes": "edited"},
	}))
	if untouched.Date == nil {
		t.Fatal("an untouched date was cleared")
	}

	// Explicit null means "remove".
	cleared := decode[gen.Item](t, do(t, s, "PATCH", itemPath(created.Id), requestOpts{
		token: ownerToken, body: map[string]any{"date": nil},
	}))
	if cleared.Date != nil || cleared.DateNormalized != nil {
		t.Errorf("date = %v / %v after clearing, want both absent", cleared.Date, cleared.DateNormalized)
	}
	if cleared.Notes == nil || *cleared.Notes != "edited" {
		t.Error("clearing the date disturbed another field")
	}
}

// TestCircaWithoutAUnitRoundTrips covers writing a date the way people
// actually write one, and confirms the inferred width comes back on the
// record rather than being recomputed by every reader.
func TestCircaWithoutAUnitRoundTrips(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "POST", itemsPath(), requestOpts{
		token: ownerToken,
		body: map[string]any{
			"typeId": "photo", "title": "Circa",
			"date": map[string]any{"kind": "circa", "date": "1960"},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	created := decode[gen.Item](t, rec)
	if created.Date == nil || created.Date.Unit == nil {
		t.Fatal("no unit was written back onto the stored date")
	}
	if *created.Date.Unit != gen.ArchiveDateUnitN5Years {
		t.Errorf("unit = %q, want 5-years for a bare year", *created.Date.Unit)
	}
	if created.DateNormalized == nil {
		t.Fatal("no normalized date")
	}
	if created.DateNormalized.DateBandTier == "" {
		t.Error("no band tier for the timeline to draw")
	}
}

func TestBadRequestsAreRejected(t *testing.T) {
	s, _ := newTestServer(t)

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{
			// A typo in a field name is told to the caller rather than
			// silently discarded.
			name: "unknown field on create", method: "POST", path: itemsPath(),
			body: map[string]any{"typeId": "photo", "title": "x", "titel": "y"},
			want: http.StatusBadRequest,
		},
		{
			name: "unknown item type", method: "POST", path: itemsPath(),
			body: map[string]any{"typeId": "hologram", "title": "x"},
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "invalid date kind", method: "POST", path: itemsPath(),
			body: map[string]any{"typeId": "photo", "title": "x",
				"date": map[string]any{"kind": "whenever", "date": "1952"}},
			want: http.StatusUnprocessableEntity,
		},
		{
			// The failure that used to be silent and ran in the worst
			// direction: an unrecognised confidence rendered as maximum
			// confidence on the map.
			name: "invalid location confidence", method: "POST", path: itemsPath(),
			body: map[string]any{"typeId": "photo", "title": "x",
				"location": map[string]any{"label": "Asheville", "lat": 35.5, "lon": -82.5, "confidence": "citty"}},
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "unknown field on patch", method: "PATCH", path: itemPath("itm_x"),
			body: map[string]any{"nonsense": 1},
			want: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, tt.method, tt.path, requestOpts{token: ownerToken, body: tt.body})
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
			if body := decode[gen.Error](t, rec); body.Error.Code == "" {
				t.Error("no error code in the envelope")
			}
		})
	}
}

func TestListRejectsUnknownQueryParameters(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "GET", itemsPath()+"?tagg=beach", requestOpts{token: ownerToken})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tagg") {
		t.Errorf("the response does not name the offending parameter: %s", rec.Body.String())
	}
}

// TestUploadURLChecksTheSlotFirst covers rejecting a bad upload before any
// bytes move, rather than after a large file has been paid for.
func TestUploadURLChecksTheSlotFirst(t *testing.T) {
	s, data := newTestServer(t)

	item := data.seedItem(testArchive, domain.Item{
		ItemCard: domain.ItemCard{TypeID: "photo", TypeVersion: 1, Title: "Target"},
	})
	path := itemPath(string(item.ID)) + "/upload-url"

	t.Run("a slot the type does not declare", func(t *testing.T) {
		rec := do(t, s, "POST", path, requestOpts{token: ownerToken, body: map[string]any{
			"role": "disc", "filename": "x.jpg", "contentType": "image/jpeg",
		}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
	})

	t.Run("media the slot does not accept", func(t *testing.T) {
		rec := do(t, s, "POST", path, requestOpts{token: ownerToken, body: map[string]any{
			"role": "photo", "filename": "x.m4a", "contentType": "audio/mp4",
		}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
	})

	t.Run("a valid upload", func(t *testing.T) {
		rec := do(t, s, "POST", path, requestOpts{token: ownerToken, body: map[string]any{
			"role": "photo", "filename": "family photo.jpg", "contentType": "image/jpeg",
		}})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}

		out := decode[struct {
			FileID    string `json:"fileId"`
			UploadURL string `json:"uploadUrl"`
			Key       string `json:"key"`
		}](t, rec)

		if out.FileID == "" {
			t.Error("no file id was minted before the upload")
		}
		// The caller's filename never reaches the key, so "../" and
		// same-name collisions are both impossible.
		if strings.Contains(out.Key, "family photo") {
			t.Errorf("key = %q, still contains the caller's filename", out.Key)
		}
		if !strings.Contains(out.Key, out.FileID) {
			t.Errorf("key = %q, does not contain the file id", out.Key)
		}
	})
}

// TestMeProvisionsAnArchive covers the seam that replaces a signup hook:
// this service is never told an account was created, so it makes an archive
// the first time it sees a verified token without one.
func TestMeProvisionsAnArchive(t *testing.T) {
	s, data := newTestServer(t)

	newAccount := domain.AccountID("acct-brand-new")
	s.Verifier = fakeVerifier{"new-token": newAccount}

	rec := do(t, s, "GET", "/v1/me?displayName=Ada", requestOpts{token: "new-token"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	me := decode[gen.Me](t, rec)
	if me.AccountId != string(newAccount) {
		t.Errorf("accountId = %q", me.AccountId)
	}
	if me.DefaultArchiveId == "" || len(me.Archives) != 1 {
		t.Fatalf("no archive was provisioned: %+v", me)
	}

	// The display name is stored, because the access token never carries
	// one - without it a members list would be a column of UUIDs.
	member, err := data.GetMember(nil, domain.ArchiveID(me.DefaultArchiveId), newAccount)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	if member.DisplayName != "Ada" {
		t.Errorf("displayName = %q, want Ada", member.DisplayName)
	}

	// A second call does not create a second archive.
	again := decode[gen.Me](t, do(t, s, "GET", "/v1/me", requestOpts{token: "new-token"}))
	if len(again.Archives) != 1 || again.DefaultArchiveId != me.DefaultArchiveId {
		t.Errorf("a second call provisioned again: %+v", again)
	}
}

func TestItemTypesAreServedAndCacheable(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "GET", "/v1/item-types", requestOpts{token: ownerToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the catalogue")
	}

	body := decode[struct {
		Data []gen.ItemType `json:"data"`
	}](t, rec)
	if len(body.Data) != 5 {
		t.Fatalf("type count = %d, want 5", len(body.Data))
	}

	// The CD is the type that proves the mechanism: several kinds of media,
	// every slot optional, composed from primitives the UI already has.
	var cd *gen.ItemType
	for i := range body.Data {
		if body.Data[i].Id == "cd" {
			cd = &body.Data[i]
		}
	}
	if cd == nil {
		t.Fatal("no cd type in the catalogue")
	}
	for _, slot := range cd.Slots {
		if slot.Min != 0 {
			t.Errorf("cd slot %q requires %d file(s); every slot should be optional", slot.Id, slot.Min)
		}
	}

	// A client that already has the catalogue is told so rather than sent it
	// again - which matters because the UI fetches this on every load to
	// build its forms.
	req := httptest.NewRequest("GET", "/v1/item-types", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	req.Header.Set("X-User-Token", ownerToken)
	req.Header.Set("If-None-Match", etag)

	cached := httptest.NewRecorder()
	s.Routes().ServeHTTP(cached, req)

	if cached.Code != http.StatusNotModified {
		t.Errorf("status with a matching If-None-Match = %d, want 304", cached.Code)
	}
	if cached.Body.Len() != 0 {
		t.Errorf("a 304 carried a %d byte body", cached.Body.Len())
	}
}
