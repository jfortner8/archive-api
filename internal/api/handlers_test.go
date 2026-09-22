package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jfortner8/archive-api/internal/authtoken"
	"github.com/jfortner8/archive-api/internal/store"
)

const (
	testAPIKey = "test-api-key"

	testSub          = "test-sub"
	testAccessToken  = "test-access-token"
	otherSub         = "other-sub"
	otherAccessToken = "other-access-token"
)

// defaultVerifier maps the two test tokens above to their accounts, so
// most tests can just call newTestServer and doRequest without thinking
// about auth at all.
func defaultVerifier() *fakeVerifier {
	return newFakeVerifier(map[string]authtoken.Claims{
		testAccessToken:  {Sub: testSub, Username: "alice"},
		otherAccessToken: {Sub: otherSub, Username: "bob"},
	})
}

func newTestServer(items *fakeItemsStore, files *fakeFilesStore) http.Handler {
	return newTestServerWithVerifier(items, files, nil)
}

func newTestServerWithVerifier(items *fakeItemsStore, files *fakeFilesStore, verifier *fakeVerifier) http.Handler {
	if items == nil {
		items = newFakeItemsStore()
	}
	if files == nil {
		files = &fakeFilesStore{}
	}
	if verifier == nil {
		verifier = defaultVerifier()
	}
	return (&Server{Items: items, Files: files, Verifier: verifier, APIKey: testAPIKey}).Routes()
}

func doRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestAs(t, h, method, path, body, testAccessToken)
}

func doRequestAs(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-API-Key", testAPIKey)
	if token != "" {
		req.Header.Set("X-User-Token", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
}

func TestHealthCheck(t *testing.T) {
	h := newTestServer(nil, nil)
	rec := doRequest(t, h, http.MethodGet, "/healthz", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	decodeJSON(t, rec, &body)
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want %q", body["status"], "ok")
	}
}

func TestRequireAPIKey(t *testing.T) {
	h := newTestServer(nil, nil)

	t.Run("healthz needs no key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("missing key is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/items", nil)
		req.Header.Set("X-User-Token", testAccessToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("wrong key is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/items", nil)
		req.Header.Set("X-API-Key", "not-the-right-key")
		req.Header.Set("X-User-Token", testAccessToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
}

func TestRequireAuth(t *testing.T) {
	h := newTestServer(nil, nil)

	t.Run("healthz needs no token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("missing token is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/items", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("empty token is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/items", nil)
		req.Header.Set("X-API-Key", testAPIKey)
		req.Header.Set("X-User-Token", "")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("unrecognized token is rejected", func(t *testing.T) {
		rec := doRequestAs(t, h, http.MethodGet, "/items", "", "not-a-real-token")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
}

func TestCreateItem(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodPost, "/items", `{"type":"photo","title":"Family photo 1952"}`)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
		}

		var item store.Item
		decodeJSON(t, rec, &item)
		if item.ID == "" {
			t.Error("expected a generated id")
		}
		if item.Type != "photo" || item.Title != "Family photo 1952" {
			t.Errorf("unexpected item: %+v", item)
		}
		if item.AccountID != testSub {
			t.Errorf("AccountID = %q, want the caller's sub %q", item.AccountID, testSub)
		}
		// Field names on the wire must be lowercase camelCase, not the Go
		// struct field names - this is what regressed once already when
		// the store.Item struct only had dynamodbav tags.
		if !strings.Contains(rec.Body.String(), `"id":`) {
			t.Errorf("expected lowercase \"id\" key in response body, got: %s", rec.Body.String())
		}
	})

	t.Run("missing fields", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodPost, "/items", `{"type":"photo"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodPost, "/items", `{not valid json`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("store error", func(t *testing.T) {
		items := newFakeItemsStore()
		items.err = errors.New("dynamodb is down")
		h := newTestServer(items, nil)

		rec := doRequest(t, h, http.MethodPost, "/items", `{"type":"photo","title":"x"}`)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
	})
}

func TestGetItem(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: testSub, Type: "photo", Title: "Test"}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodGet, "/items/item-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		var item store.Item
		decodeJSON(t, rec, &item)
		if item.ID != "item-1" {
			t.Errorf("id = %q, want %q", item.ID, "item-1")
		}
	})

	t.Run("not found", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodGet, "/items/missing", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestListItems(t *testing.T) {
	seed1 := store.Item{ID: "item-1", AccountID: testSub, Type: "photo", Title: "A"}
	seed2 := store.Item{ID: "item-2", AccountID: testSub, Type: "document", Title: "B"}
	h := newTestServer(newFakeItemsStore(seed1, seed2), nil)

	rec := doRequest(t, h, http.MethodGet, "/items", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var items []store.Item
	decodeJSON(t, rec, &items)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
}

func TestPresignUpload(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: testSub, Type: "photo", Title: "Test"}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodPost, "/items/item-1/upload-url",
			`{"role":"front","filename":"front.jpg","contentType":"image/jpeg"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp presignUploadResponse
		decodeJSON(t, rec, &resp)
		if resp.UploadURL == "" || resp.Key == "" {
			t.Errorf("unexpected response: %+v", resp)
		}
		if !strings.Contains(resp.Key, "item-1") || !strings.Contains(resp.Key, "front") {
			t.Errorf("key %q should reference item id and role", resp.Key)
		}
		if !strings.Contains(resp.Key, testSub) {
			t.Errorf("key %q should reference the caller's account", resp.Key)
		}
	})

	t.Run("item not found", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodPost, "/items/missing/upload-url",
			`{"role":"front","filename":"front.jpg","contentType":"image/jpeg"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("missing fields", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: testSub}
		h := newTestServer(newFakeItemsStore(seed), nil)
		rec := doRequest(t, h, http.MethodPost, "/items/item-1/upload-url", `{"role":"front"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("another account's item is not found", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: otherSub, Type: "photo", Title: "Test"}
		h := newTestServer(newFakeItemsStore(seed), nil)
		rec := doRequest(t, h, http.MethodPost, "/items/item-1/upload-url",
			`{"role":"front","filename":"front.jpg","contentType":"image/jpeg"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestAttachFile(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: testSub, Type: "photo", Title: "Test"}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodPost, "/items/item-1/files",
			`{"role":"front","key":"items/item-1/front-x.jpg","contentType":"image/jpeg","sizeBytes":123}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var item store.Item
		decodeJSON(t, rec, &item)
		if len(item.Files) != 1 || item.Files[0].Role != "front" {
			t.Errorf("unexpected files: %+v", item.Files)
		}
		if item.Files[0].ID == "" {
			t.Error("expected the attached file to get a generated id")
		}
	})

	t.Run("item not found", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodPost, "/items/missing/files",
			`{"role":"front","key":"x"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("another account's item is not found", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: otherSub, Type: "photo", Title: "Test"}
		h := newTestServer(newFakeItemsStore(seed), nil)
		rec := doRequest(t, h, http.MethodPost, "/items/item-1/files",
			`{"role":"front","key":"x"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestUpdateItem(t *testing.T) {
	t.Run("partial update only touches provided fields", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: testSub, Type: "photo", Title: "Original title", Notes: "original notes"}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodPatch, "/items/item-1",
			`{"title":"New title","tags":["family","1952"]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var item store.Item
		decodeJSON(t, rec, &item)
		if item.Title != "New title" {
			t.Errorf("Title = %q, want %q", item.Title, "New title")
		}
		if item.Notes != "original notes" {
			t.Errorf("Notes = %q, want it left unchanged (%q)", item.Notes, "original notes")
		}
		if len(item.Tags) != 2 || item.Tags[0] != "family" {
			t.Errorf("Tags = %v, want [family 1952]", item.Tags)
		}
	})

	t.Run("sets date and location", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: testSub, Type: "photo", Title: "Test"}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodPatch, "/items/item-1",
			`{"date":{"kind":"circa","date":"1985-01-01","unit":"decade"},"location":{"label":"Asheville, NC","lat":35.6,"lon":-82.5}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var item store.Item
		decodeJSON(t, rec, &item)
		if item.Date == nil || item.Date.Kind != "circa" {
			t.Errorf("Date = %+v, want kind=circa", item.Date)
		}
		if item.Location == nil || item.Location.Label != "Asheville, NC" {
			t.Errorf("Location = %+v, want label=Asheville, NC", item.Location)
		}
	})

	t.Run("item not found", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodPatch, "/items/missing", `{"title":"x"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: testSub}
		h := newTestServer(newFakeItemsStore(seed), nil)
		rec := doRequest(t, h, http.MethodPatch, "/items/item-1", `{not valid`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("another account's item is not found", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: otherSub, Type: "photo", Title: "Test"}
		h := newTestServer(newFakeItemsStore(seed), nil)
		rec := doRequest(t, h, http.MethodPatch, "/items/item-1", `{"title":"hijacked"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestPresignDownload(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		seed := store.Item{
			ID:        "item-1",
			AccountID: testSub,
			Files:     []store.File{{ID: "file-1", Role: "front", Key: "items/item-1/front-x.jpg"}},
		}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodGet, "/items/item-1/files/file-1/download-url", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp map[string]string
		decodeJSON(t, rec, &resp)
		if resp["downloadUrl"] == "" {
			t.Errorf("expected a downloadUrl, got: %+v", resp)
		}
	})

	t.Run("distinguishes files sharing a role", func(t *testing.T) {
		// e.g. two voice memos, or multi-page documents - role alone
		// isn't a unique key, only the file's own ID is.
		seed := store.Item{
			ID:        "item-1",
			AccountID: testSub,
			Files: []store.File{
				{ID: "file-1", Role: "page", Order: 1, Key: "items/item-1/page-1.jpg"},
				{ID: "file-2", Role: "page", Order: 2, Key: "items/item-1/page-2.jpg"},
			},
		}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodGet, "/items/item-1/files/file-2/download-url", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp map[string]string
		decodeJSON(t, rec, &resp)
		if !strings.Contains(resp["downloadUrl"], "page-2.jpg") {
			t.Errorf("expected the download URL for file-2's key, got: %+v", resp)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		seed := store.Item{ID: "item-1", AccountID: testSub, Files: []store.File{{ID: "file-1", Role: "front", Key: "x"}}}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodGet, "/items/item-1/files/no-such-file/download-url", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("item not found", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodGet, "/items/missing/files/file-1/download-url", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("another account's item is not found", func(t *testing.T) {
		seed := store.Item{
			ID:        "item-1",
			AccountID: otherSub,
			Files:     []store.File{{ID: "file-1", Role: "front", Key: "x"}},
		}
		h := newTestServer(newFakeItemsStore(seed), nil)
		rec := doRequest(t, h, http.MethodGet, "/items/item-1/files/file-1/download-url", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

// TestItemOwnership is the end-to-end cross-account check: everything one
// account creates should be completely invisible to another account,
// across every route that touches an item.
func TestItemOwnership(t *testing.T) {
	items := newFakeItemsStore()
	h := newTestServer(items, nil)

	createRec := doRequestAs(t, h, http.MethodPost, "/items", `{"type":"photo","title":"mine"}`, testAccessToken)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	var created store.Item
	decodeJSON(t, createRec, &created)

	t.Run("List excludes another account's items", func(t *testing.T) {
		rec := doRequestAs(t, h, http.MethodGet, "/items", "", otherAccessToken)
		var items []store.Item
		decodeJSON(t, rec, &items)
		if len(items) != 0 {
			t.Errorf("expected no items visible to another account, got %+v", items)
		}
	})

	t.Run("Get 404s for another account", func(t *testing.T) {
		rec := doRequestAs(t, h, http.MethodGet, "/items/"+created.ID, "", otherAccessToken)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("Update 404s for another account", func(t *testing.T) {
		rec := doRequestAs(t, h, http.MethodPatch, "/items/"+created.ID, `{"title":"hijacked"}`, otherAccessToken)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("upload-url 404s for another account", func(t *testing.T) {
		rec := doRequestAs(t, h, http.MethodPost, "/items/"+created.ID+"/upload-url",
			`{"role":"front","filename":"x.jpg","contentType":"image/jpeg"}`, otherAccessToken)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("attach file 404s for another account", func(t *testing.T) {
		rec := doRequestAs(t, h, http.MethodPost, "/items/"+created.ID+"/files",
			`{"role":"front","key":"x"}`, otherAccessToken)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("owner can still see it", func(t *testing.T) {
		rec := doRequestAs(t, h, http.MethodGet, "/items/"+created.ID, "", testAccessToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}
