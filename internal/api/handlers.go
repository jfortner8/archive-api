// Package api wires HTTP requests to the item store and file storage.
package api

import (
	"context"
	"crypto/subtle"
	"net/http"

	"github.com/jfortner8/archive-api/internal/storage"
	"github.com/jfortner8/archive-api/internal/store"
)

// itemsStore is the subset of *store.ItemStore the handlers need. Defining
// it here (rather than depending on the concrete type) lets tests supply a
// fake instead of talking to real DynamoDB.
type itemsStore interface {
	Create(ctx context.Context, itemType, title string) (store.Item, error)
	Get(ctx context.Context, id string) (store.Item, error)
	List(ctx context.Context) ([]store.Item, error)
	AddFile(ctx context.Context, id string, file store.File) (store.Item, error)
}

// filesStore is the subset of *storage.FileStore the handlers need.
type filesStore interface {
	PresignUpload(ctx context.Context, key, contentType string) (string, error)
	PresignDownload(ctx context.Context, key string) (string, error)
}

// Server holds the dependencies HTTP handlers need.
type Server struct {
	Items itemsStore
	Files filesStore

	// APIKey must match the X-API-Key header on every route except
	// /healthz. There's no other access control on this API.
	APIKey string
}

// Routes returns the HTTP handler for the whole API.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthCheck)

	mux.HandleFunc("POST /items", s.requireAPIKey(s.createItem))
	mux.HandleFunc("GET /items", s.requireAPIKey(s.listItems))
	mux.HandleFunc("GET /items/{id}", s.requireAPIKey(s.getItem))

	mux.HandleFunc("POST /items/{id}/upload-url", s.requireAPIKey(s.presignUpload))
	mux.HandleFunc("POST /items/{id}/files", s.requireAPIKey(s.attachFile))
	mux.HandleFunc("GET /items/{id}/files/{role}/download-url", s.requireAPIKey(s.presignDownload))

	return mux
}

// requireAPIKey rejects any request whose X-API-Key header doesn't match
// s.APIKey. Uses a constant-time comparison so response timing can't be
// used to guess the key one byte at a time.
func (s *Server) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-API-Key")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid or missing API key")
			return
		}
		next(w, r)
	}
}

func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type createItemRequest struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}

func (s *Server) createItem(w http.ResponseWriter, r *http.Request) {
	var req createItemRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Type == "" || req.Title == "" {
		writeError(w, http.StatusBadRequest, "type and title are required")
		return
	}

	item, err := s.Items.Create(r.Context(), req.Type, req.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create item")
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.Items.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list items")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	item, err := s.Items.Get(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get item")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type presignUploadRequest struct {
	Role        string `json:"role"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
}

type presignUploadResponse struct {
	UploadURL string `json:"uploadUrl"`
	Key       string `json:"key"`
}

func (s *Server) presignUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req presignUploadRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role == "" || req.Filename == "" || req.ContentType == "" {
		writeError(w, http.StatusBadRequest, "role, filename, and contentType are required")
		return
	}

	// Confirm the item exists before handing out a URL for it.
	if _, err := s.Items.Get(r.Context(), id); err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to look up item")
		return
	}

	key := storage.BuildKey(id, req.Role, req.Filename)
	url, err := s.Files.PresignUpload(r.Context(), key, req.ContentType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create upload URL")
		return
	}

	writeJSON(w, http.StatusOK, presignUploadResponse{UploadURL: url, Key: key})
}

type attachFileRequest struct {
	Role        string `json:"role"`
	Key         string `json:"key"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

// attachFile records that a file was successfully uploaded to S3 (call
// this after the client's PUT to the presigned URL succeeds).
func (s *Server) attachFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req attachFileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "role and key are required")
		return
	}

	item, err := s.Items.AddFile(r.Context(), id, store.File{
		Role:        req.Role,
		Key:         req.Key,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
	})
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to attach file")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) presignDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	role := r.PathValue("role")

	item, err := s.Items.Get(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get item")
		return
	}

	for _, f := range item.Files {
		if f.Role == role {
			url, err := s.Files.PresignDownload(r.Context(), f.Key)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create download URL")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"downloadUrl": url})
			return
		}
	}
	writeError(w, http.StatusNotFound, "file not found for that role")
}
