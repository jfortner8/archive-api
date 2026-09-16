package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jfortner8/archive-api/internal/store"
)

const testAPIKey = "test-api-key"

func newTestServer(items *fakeItemsStore, files *fakeFilesStore) http.Handler {
	if items == nil {
		items = newFakeItemsStore()
	}
	if files == nil {
		files = &fakeFilesStore{}
	}
	return (&Server{Items: items, Files: files, APIKey: testAPIKey}).Routes()
}

func doRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-API-Key", testAPIKey)
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
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("wrong key is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/items", nil)
		req.Header.Set("X-API-Key", "not-the-right-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
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
		seed := store.Item{ID: "item-1", Type: "photo", Title: "Test"}
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
	seed1 := store.Item{ID: "item-1", Type: "photo", Title: "A"}
	seed2 := store.Item{ID: "item-2", Type: "document", Title: "B"}
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
		seed := store.Item{ID: "item-1", Type: "photo", Title: "Test"}
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
		seed := store.Item{ID: "item-1"}
		h := newTestServer(newFakeItemsStore(seed), nil)
		rec := doRequest(t, h, http.MethodPost, "/items/item-1/upload-url", `{"role":"front"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}

func TestAttachFile(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		seed := store.Item{ID: "item-1", Type: "photo", Title: "Test"}
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
	})

	t.Run("item not found", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodPost, "/items/missing/files",
			`{"role":"front","key":"x"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestPresignDownload(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		seed := store.Item{
			ID:    "item-1",
			Files: []store.File{{Role: "front", Key: "items/item-1/front-x.jpg"}},
		}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodGet, "/items/item-1/files/front/download-url", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp map[string]string
		decodeJSON(t, rec, &resp)
		if resp["downloadUrl"] == "" {
			t.Errorf("expected a downloadUrl, got: %+v", resp)
		}
	})

	t.Run("role not found", func(t *testing.T) {
		seed := store.Item{ID: "item-1", Files: []store.File{{Role: "front", Key: "x"}}}
		h := newTestServer(newFakeItemsStore(seed), nil)

		rec := doRequest(t, h, http.MethodGet, "/items/item-1/files/back/download-url", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("item not found", func(t *testing.T) {
		h := newTestServer(nil, nil)
		rec := doRequest(t, h, http.MethodGet, "/items/missing/files/front/download-url", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}
