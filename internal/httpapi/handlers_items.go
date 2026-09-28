package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
	"github.com/jfortner8/archive-api/internal/storage"
	"github.com/jfortner8/archive-api/internal/store"
)

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) error {
	member, ok := memberFrom(r.Context())
	if !ok {
		return errInternal()
	}

	opts, err := parseListOptions(r)
	if err != nil {
		return err
	}

	page, err := s.Store.ListItems(r.Context(), member.ArchiveID, opts)
	if err != nil {
		return err
	}

	urls := s.resolveCoverURLs(r.Context(), page.Items)

	cards := make([]gen.ItemCard, 0, len(page.Items))
	for _, card := range page.Items {
		cards = append(cards, toGenCard(card, urls))
	}

	return writeJSON(w, http.StatusOK, struct {
		Data []gen.ItemCard `json:"data"`
		Page gen.PageInfo   `json:"page"`
	}{
		Data: cards,
		Page: gen.PageInfo{
			Cursor:  optional(page.NextCursor),
			HasMore: page.NextCursor != "",
			Limit:   opts.Limit,
		},
	})
}

func parseListOptions(r *http.Request) (store.ListOptions, error) {
	query := r.URL.Query()

	// An unrecognised parameter is an error rather than something ignored.
	// A client that misspells a filter should be told, not quietly served
	// the unfiltered set and left to wonder.
	for name := range query {
		switch name {
		case "limit", "cursor", "sort":
		default:
			return store.ListOptions{}, errBadRequest(fmt.Sprintf("unknown query parameter %q", name))
		}
	}

	opts := store.ListOptions{Limit: 60}

	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return store.ListOptions{}, errBadRequest("limit must be between 1 and 200")
		}
		opts.Limit = limit
	}

	switch sort := query.Get("sort"); sort {
	case "", "date:desc":
	case "date:asc":
		opts.Ascending = true
	default:
		return store.ListOptions{}, errBadRequest(`sort must be "date:desc" or "date:asc"`)
	}

	opts.Cursor = query.Get("cursor")
	return opts, nil
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	item, err := s.Store.GetItem(r.Context(), member.ArchiveID, domain.ItemID(r.PathValue("itemId")))
	if err != nil {
		return err
	}
	return s.writeItem(w, r, http.StatusOK, item)
}

func (s *Server) createItem(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	var body gen.ItemCreate
	if err := readJSON(r, &body); err != nil {
		return err
	}

	item := &domain.Item{
		ItemCard: domain.ItemCard{
			ID:        domain.NewItemID(),
			ArchiveID: member.ArchiveID,
			TypeID:    body.TypeId,
			Title:     body.Title,
			Date:      fromGenDate(body.Date),
			Location:  fromGenLocation(body.Location),
			Subjects:  fromGenSubjects(body.Subjects),
		},
	}
	if body.PlaceId != nil {
		item.PlaceID = domain.PlaceID(*body.PlaceId)
	}
	if body.Notes != nil {
		item.Notes = *body.Notes
	}
	if body.Tags != nil {
		item.Tags = *body.Tags
	}
	if body.Attributes != nil {
		item.Attributes = *body.Attributes
	}

	// The type's current version is recorded, so a later narrowing change to
	// that type can be detected rather than silently misapplied.
	if typ, ok := s.Types.Get(body.TypeId); ok {
		item.TypeVersion = typ.Version
	}

	if err := s.Store.CreateItem(r.Context(), item); err != nil {
		return err
	}
	return s.writeItem(w, r, http.StatusCreated, *item)
}

// patchFields is the set PATCH accepts. Decoding through a raw map first is
// what makes "clear this field" expressible: a pointer alone cannot tell an
// absent key from an explicit null, which is why the previous API had no way
// to remove a date once set.
var patchFields = map[string]bool{
	"title": true, "date": true, "location": true, "placeId": true,
	"notes": true, "tags": true, "subjects": true, "attributes": true,
	"coverFileId": true,
}

func (s *Server) patchItem(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	ifVersion, err := parseIfMatch(r)
	if err != nil {
		return err
	}

	raw := map[string]json.RawMessage{}
	if err := readJSON(r, &raw); err != nil {
		return err
	}
	for name := range raw {
		if !patchFields[name] {
			return errBadRequest(fmt.Sprintf("unknown field %q", name))
		}
	}

	patch, err := buildPatch(raw)
	if err != nil {
		return err
	}

	item, err := s.Store.PatchItem(r.Context(), member.ArchiveID, domain.ItemID(r.PathValue("itemId")), patch, ifVersion)
	if err != nil {
		return err
	}
	return s.writeItem(w, r, http.StatusOK, item)
}

// buildPatch turns the decoded body into a domain patch. `null` clears;
// absent leaves alone.
func buildPatch(raw map[string]json.RawMessage) (domain.ItemPatch, error) {
	var patch domain.ItemPatch

	decode := func(field string, into any) (present bool, err error) {
		value, ok := raw[field]
		if !ok {
			return false, nil
		}
		if string(value) == "null" {
			return true, nil
		}
		if err := json.Unmarshal(value, into); err != nil {
			return false, errBadRequest(fmt.Sprintf("%s is not valid", field))
		}
		return true, nil
	}

	if value, ok := raw["title"]; ok {
		var title string
		if err := json.Unmarshal(value, &title); err != nil {
			return patch, errBadRequest("title is not valid")
		}
		// Title is the one field with no meaningful empty state: an item
		// without one is unfindable, so clearing it is refused.
		patch.Title = domain.SetTo(title)
	}

	var date gen.ArchiveDate
	if present, err := decode("date", &date); err != nil {
		return patch, err
	} else if present {
		if d := fromGenDate(&date); d != nil && date.Kind != "" {
			patch.Date = domain.SetTo(*d)
		} else {
			patch.Date = domain.Clear[domain.ArchiveDate]()
		}
	}

	var location gen.ArchiveLocation
	if present, err := decode("location", &location); err != nil {
		return patch, err
	} else if present {
		if location.Label != "" {
			patch.Location = domain.SetTo(*fromGenLocation(&location))
		} else {
			patch.Location = domain.Clear[domain.Location]()
		}
	}

	var placeID string
	if present, err := decode("placeId", &placeID); err != nil {
		return patch, err
	} else if present {
		if placeID == "" {
			patch.PlaceID = domain.Clear[domain.PlaceID]()
		} else {
			patch.PlaceID = domain.SetTo(domain.PlaceID(placeID))
		}
	}

	var notes string
	if present, err := decode("notes", &notes); err != nil {
		return patch, err
	} else if present {
		patch.Notes = domain.SetTo(notes)
	}

	var tags []string
	if present, err := decode("tags", &tags); err != nil {
		return patch, err
	} else if present {
		if tags == nil {
			tags = []string{}
		}
		patch.Tags = domain.SetTo(tags)
	}

	var subjects []gen.SubjectRef
	if present, err := decode("subjects", &subjects); err != nil {
		return patch, err
	} else if present {
		patch.Subjects = domain.SetTo(fromGenSubjects(&subjects))
	}

	var attributes map[string]any
	if present, err := decode("attributes", &attributes); err != nil {
		return patch, err
	} else if present {
		patch.Attributes = domain.SetTo(attributes)
	}

	var coverFileID string
	if present, err := decode("coverFileId", &coverFileID); err != nil {
		return patch, err
	} else if present {
		if coverFileID == "" {
			// Handing the choice back to the server, which re-derives it
			// from the type's preference order.
			patch.CoverFileID = domain.Clear[domain.FileID]()
		} else {
			patch.CoverFileID = domain.SetTo(domain.FileID(coverFileID))
		}
	}

	return patch, nil
}

func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	ifVersion, err := parseIfMatch(r)
	if err != nil {
		return err
	}

	if err := s.Store.DeleteItem(r.Context(), member.ArchiveID, domain.ItemID(r.PathValue("itemId")), ifVersion); err != nil {
		return err
	}

	// The S3 objects are deliberately left for a later sweep. An orphaned
	// object is invisible and cheap to clean up; an orphaned record is a
	// broken image in the gallery, so the record goes first.
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// writeItem is the single exit for every item response, so the ETag can
// never be forgotten on one route and present on another.
func (s *Server) writeItem(w http.ResponseWriter, r *http.Request, status int, item domain.Item) error {
	urls := s.resolveItemURLs(r.Context(), item)
	w.Header().Set("ETag", etagFor(item.Version))
	return writeJSON(w, status, toGenItem(item, urls))
}

// resolveItemURLs presigns every file on one item.
func (s *Server) resolveItemURLs(ctx context.Context, item domain.Item) fileURLs {
	urls := fileURLs{
		byFile:    make(map[domain.FileID]string, len(item.Files)),
		expiresIn: int(storage.DownloadURLExpiry.Seconds()),
	}
	for _, f := range item.Files {
		if f.Key == "" {
			continue
		}
		if url, err := s.Files.PresignDownload(ctx, f.Key); err == nil {
			urls.byFile[f.ID] = url
		}
	}
	return urls
}

// resolveCoverURLs presigns just the cover of each card in a page.
//
// A gallery needs one image per tile, not every file of every item. The
// previous client asked for all of them, one request each, and blocked its
// first paint on the lot - presigning sixty covers here instead is a few
// milliseconds of local arithmetic inside a request that was happening
// anyway.
func (s *Server) resolveCoverURLs(ctx context.Context, cards []domain.ItemCard) fileURLs {
	urls := fileURLs{
		byFile:    make(map[domain.FileID]string, len(cards)),
		expiresIn: int(storage.DownloadURLExpiry.Seconds()),
	}
	for _, card := range cards {
		if card.CoverKey == "" || card.CoverFileID == "" {
			continue
		}
		if url, err := s.Files.PresignDownload(ctx, card.CoverKey); err == nil {
			urls.byFile[card.CoverFileID] = url
		}
	}
	return urls
}
