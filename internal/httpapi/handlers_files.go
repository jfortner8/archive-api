package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
	"github.com/jfortner8/archive-api/internal/storage"
)

// createUploadURL mints the file id, checks the slot, and hands back a
// presigned PUT.
//
// Checking before issuing the URL matters: the alternative is discovering
// that a 200 MB video does not belong in a photo slot only after it has been
// uploaded and paid for.
func (s *Server) createUploadURL(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())
	itemID := domain.ItemID(r.PathValue("itemId"))

	var body struct {
		Role        string `json:"role"`
		Filename    string `json:"filename"`
		ContentType string `json:"contentType"`
		SizeBytes   int64  `json:"sizeBytes,omitempty"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	if body.Role == "" || body.Filename == "" || body.ContentType == "" {
		return errBadRequest("role, filename and contentType are all required")
	}

	item, err := s.Store.GetItem(r.Context(), member.ArchiveID, itemID)
	if err != nil {
		return err
	}

	typ, ok := s.Types.Get(item.TypeID)
	if !ok {
		return errInternal()
	}

	slot, ok := typ.Slot(body.Role)
	if !ok {
		return errValidation(domain.ValidationErrors{{
			Field: "role", Code: "unknown_slot",
			Message: fmt.Sprintf("%q declares no slot %q", typ.ID, body.Role),
		}})
	}

	family, err := itemtypes.FamilyForContentType(body.ContentType)
	if err != nil {
		return errValidation(domain.ValidationErrors{{
			Field: "contentType", Code: "unsupported_media", Message: err.Error(),
		}})
	}
	if !slot.AcceptsFamily(family) {
		return errValidation(domain.ValidationErrors{{
			Field: "contentType", Code: "wrong_media_family",
			Message: fmt.Sprintf("slot %q accepts %v, not %q", slot.ID, slot.Accepts, family),
		}})
	}
	if slot.MaxSizeBytes > 0 && body.SizeBytes > slot.MaxSizeBytes {
		return errValidation(domain.ValidationErrors{{
			Field: "sizeBytes", Code: "too_large",
			Message: fmt.Sprintf("slot %q allows at most %d bytes", slot.ID, slot.MaxSizeBytes),
		}})
	}
	if len(item.FilesInSlot(slot.ID)) >= slot.Max {
		return errValidation(domain.ValidationErrors{{
			Field: "role", Code: "slot_full",
			Message: fmt.Sprintf("slot %q already holds its maximum of %d file(s)", slot.ID, slot.Max),
		}})
	}

	fileID := domain.NewFileID()
	key := storage.ObjectKey(
		string(member.ArchiveID), string(itemID), string(fileID),
		storage.ExtensionFor(body.ContentType),
	)

	url, err := s.Files.PresignUpload(r.Context(), key, body.ContentType)
	if err != nil {
		return err
	}

	return writeJSON(w, http.StatusOK, struct {
		FileID    string `json:"fileId"`
		UploadURL string `json:"uploadUrl"`
		Key       string `json:"key"`
		ExpiresIn int    `json:"expiresIn"`
	}{
		FileID:    string(fileID),
		UploadURL: url,
		Key:       key,
		ExpiresIn: int(storage.UploadURLExpiry.Seconds()),
	})
}

// attachFile records that an upload finished.
//
// Idempotent, because the id was chosen by the server before the upload: a
// retried confirmation attaches one file rather than a duplicate.
func (s *Server) attachFile(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())
	itemID := domain.ItemID(r.PathValue("itemId"))

	var body struct {
		FileID           string `json:"fileId"`
		Role             string `json:"role"`
		Key              string `json:"key"`
		ContentType      string `json:"contentType"`
		SizeBytes        int64  `json:"sizeBytes,omitempty"`
		OriginalFilename string `json:"originalFilename,omitempty"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	if body.FileID == "" || body.Role == "" || body.Key == "" || body.ContentType == "" {
		return errBadRequest("fileId, role, key and contentType are all required")
	}

	// The key has to be the one this API issued for this file, on this item,
	// in this archive. Otherwise a caller could attach a record pointing at
	// any object in the bucket.
	expectedPrefix := storage.ObjectKey(string(member.ArchiveID), string(itemID), body.FileID, "")
	if !hasPrefix(body.Key, expectedPrefix) {
		return errBadRequest("key does not belong to this file")
	}

	item, err := s.Store.AppendFile(r.Context(), member.ArchiveID, itemID, domain.File{
		ID:               domain.FileID(body.FileID),
		Role:             body.Role,
		Key:              body.Key,
		ContentType:      body.ContentType,
		SizeBytes:        body.SizeBytes,
		OriginalFilename: body.OriginalFilename,
		Status:           domain.FileStatusReady,
	})
	if err != nil {
		return err
	}
	return s.writeItem(w, r, http.StatusOK, item)
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func (s *Server) reorderFiles(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	ifVersion, err := parseIfMatch(r)
	if err != nil {
		return err
	}

	var body struct {
		FileIDs []string `json:"fileIds"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}

	order := make([]domain.FileID, 0, len(body.FileIDs))
	for _, id := range body.FileIDs {
		order = append(order, domain.FileID(id))
	}

	item, err := s.Store.ReorderFiles(r.Context(), member.ArchiveID, domain.ItemID(r.PathValue("itemId")), order, ifVersion)
	if err != nil {
		// A list that omits or repeats a file is the caller's mistake, not a
		// server fault, and saying which is more useful than a 500.
		if _, isAPI := err.(*apiError); !isAPI && isReorderComplaint(err) {
			return errBadRequest(err.Error())
		}
		return err
	}
	return s.writeItem(w, r, http.StatusOK, item)
}

func isReorderComplaint(err error) bool {
	msg := err.Error()
	return hasPrefix(msg, "reorder ")
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	ifVersion, err := parseIfMatch(r)
	if err != nil {
		return err
	}

	item, err := s.Store.DeleteFile(
		r.Context(),
		member.ArchiveID,
		domain.ItemID(r.PathValue("itemId")),
		domain.FileID(r.PathValue("fileId")),
		ifVersion,
	)
	if err != nil {
		return err
	}

	// As with deleting an item: the record goes first and the S3 object is
	// swept later. An orphaned object is invisible; an orphaned record is a
	// broken image.
	return s.writeItem(w, r, http.StatusOK, item)
}

// typesETag identifies the compiled-in catalogue. Computed once, because it
// cannot change without a redeploy.
var typesETagOnce struct {
	sync.Once
	value string
}

func (s *Server) typesETag() string {
	typesETagOnce.Do(func() {
		hash := sha256.New()
		for _, t := range s.Types.All() {
			fmt.Fprintf(hash, "%s:%d;", t.ID, t.Version)
			for _, slot := range t.Slots {
				fmt.Fprintf(hash, "%s,", slot.ID)
			}
		}
		typesETagOnce.value = `W/"` + hex.EncodeToString(hash.Sum(nil))[:16] + `"`
	})
	return typesETagOnce.value
}
