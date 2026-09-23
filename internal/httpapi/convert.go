package httpapi

import (
	"time"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
)

func ptr[T any](v T) *T { return &v }

// optional returns nil for a zero value, so an absent string does not
// serialize as "".
func optional[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// fileURLs is what a handler resolves before converting an item: a presigned
// download URL per file, plus how long they last.
//
// They are resolved in bulk and returned inline rather than fetched one
// request at a time. Presigning is local HMAC arithmetic, not a network call,
// so doing sixty of them costs microseconds - whereas asking the client to
// fetch sixty URLs cost it sixty round trips before anything rendered.
type fileURLs struct {
	byFile    map[domain.FileID]string
	expiresIn int
}

func toGenItem(item domain.Item, urls fileURLs) gen.Item {
	card := toGenCard(item.ItemCard, urls)

	files := make([]gen.File, 0, len(item.Files))
	for _, f := range item.Files {
		files = append(files, toGenFile(f, urls))
	}

	out := gen.Item{
		Id:             card.Id,
		ArchiveId:      card.ArchiveId,
		TypeId:         card.TypeId,
		TypeVersion:    card.TypeVersion,
		Title:          card.Title,
		Date:           card.Date,
		DateNormalized: card.DateNormalized,
		Location:       card.Location,
		PlaceId:        card.PlaceId,
		Tags:           card.Tags,
		Subjects:       card.Subjects,
		Capabilities:   card.Capabilities,
		CoverFileId:    card.CoverFileId,
		CoverPinned:    card.CoverPinned,
		CoverUrl:       card.CoverUrl,
		CoverWidth:     card.CoverWidth,
		CoverHeight:    card.CoverHeight,
		FileCount:      card.FileCount,
		CreatedAt:      card.CreatedAt,
		UpdatedAt:      card.UpdatedAt,
		Notes:          optional(item.Notes),
		Files:          files,
	}
	if len(item.Attributes) > 0 {
		out.Attributes = &item.Attributes
	}
	return out
}

func toGenCard(card domain.ItemCard, urls fileURLs) gen.ItemCard {
	tags := card.Tags
	if tags == nil {
		tags = []string{}
	}

	subjects := make([]gen.SubjectRef, 0, len(card.Subjects))
	for _, s := range card.Subjects {
		subjects = append(subjects, gen.SubjectRef{
			Id: string(s.ID), Kind: s.Kind, Name: s.Name,
		})
	}

	capabilities := make([]gen.MediaFamily, 0, len(card.Capabilities))
	for _, c := range card.Capabilities {
		capabilities = append(capabilities, gen.MediaFamily(c))
	}

	out := gen.ItemCard{
		Id:             string(card.ID),
		ArchiveId:      string(card.ArchiveID),
		TypeId:         card.TypeID,
		TypeVersion:    card.TypeVersion,
		Title:          card.Title,
		Date:           toGenDate(card.Date),
		DateNormalized: toGenDateNormalized(card.DateNorm),
		Location:       toGenLocation(card.Location),
		PlaceId:        optional(string(card.PlaceID)),
		Tags:           tags,
		Subjects:       subjects,
		Capabilities:   capabilities,
		CoverFileId:    optional(string(card.CoverFileID)),
		CoverWidth:     optional(card.CoverWidth),
		CoverHeight:    optional(card.CoverHeight),
		FileCount:      card.FileCount,
		CreatedAt:      card.CreatedAt,
		UpdatedAt:      card.UpdatedAt,
	}
	if card.CoverPinned {
		out.CoverPinned = ptr(true)
	}
	if url, ok := urls.byFile[card.CoverFileID]; ok {
		out.CoverUrl = ptr(url)
	}
	return out
}

func toGenFile(f domain.File, urls fileURLs) gen.File {
	out := gen.File{
		Id:               string(f.ID),
		Role:             f.Role,
		ContentType:      f.ContentType,
		SizeBytes:        f.SizeBytes,
		OriginalFilename: optional(f.OriginalFilename),
		Width:            optional(f.Width),
		Height:           optional(f.Height),
		Status:           gen.FileStatus(f.Status),
	}
	if url, ok := urls.byFile[f.ID]; ok {
		out.Url = ptr(url)
		out.UrlExpiresIn = ptr(urls.expiresIn)
	}
	return out
}

func toGenDate(d *domain.ArchiveDate) *gen.ArchiveDate {
	if d == nil {
		return nil
	}
	out := &gen.ArchiveDate{
		Kind:  gen.ArchiveDateKind(d.Kind),
		Date:  optional(d.Date),
		Start: optional(d.Start),
		End:   optional(d.End),
	}
	if d.Unit != "" {
		out.Unit = ptr(gen.ArchiveDateUnit(d.Unit))
	}
	return out
}

func fromGenDate(d *gen.ArchiveDate) *domain.ArchiveDate {
	if d == nil {
		return nil
	}
	out := &domain.ArchiveDate{Kind: domain.DateKind(d.Kind)}
	if d.Date != nil {
		out.Date = *d.Date
	}
	if d.Start != nil {
		out.Start = *d.Start
	}
	if d.End != nil {
		out.End = *d.End
	}
	if d.Unit != nil {
		out.Unit = domain.CircaUnit(*d.Unit)
	}
	return out
}

// toGenDateNormalized parses the stored instants back into times. A value
// that cannot be parsed is dropped rather than returned as a zero time,
// which would read as the year 1.
func toGenDateNormalized(n *domain.Normalized) *gen.DateNormalized {
	if n == nil {
		return nil
	}

	sort, err1 := time.Parse(time.RFC3339Nano, n.Sort)
	earliest, err2 := time.Parse(time.RFC3339Nano, n.Earliest)
	latest, err3 := time.Parse(time.RFC3339Nano, n.Latest)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil
	}

	return &gen.DateNormalized{
		DateSort:      sort,
		DateEarliest:  earliest,
		DateLatest:    latest,
		DatePrecision: gen.DateNormalizedDatePrecision(n.Precision),
		DateBandTier:  gen.DateNormalizedDateBandTier(n.BandTier),
	}
}

func toGenLocation(l *domain.Location) *gen.ArchiveLocation {
	if l == nil {
		return nil
	}
	out := &gen.ArchiveLocation{
		Label:        l.Label,
		Lat:          l.Lat,
		Lon:          l.Lon,
		RadiusMeters: optional(l.RadiusMeters),
	}
	if l.Confidence != "" {
		out.Confidence = ptr(gen.ArchiveLocationConfidence(l.Confidence))
	}
	return out
}

func fromGenLocation(l *gen.ArchiveLocation) *domain.Location {
	if l == nil {
		return nil
	}
	out := &domain.Location{Label: l.Label, Lat: l.Lat, Lon: l.Lon}
	if l.Confidence != nil {
		out.Confidence = domain.ConfidenceRadius(*l.Confidence)
	}
	return out
}

func fromGenSubjects(refs *[]gen.SubjectRef) []domain.SubjectRef {
	if refs == nil {
		return nil
	}
	out := make([]domain.SubjectRef, 0, len(*refs))
	for _, s := range *refs {
		out = append(out, domain.SubjectRef{
			ID: domain.SubjectID(s.Id), Kind: s.Kind, Name: s.Name,
		})
	}
	return out
}

func toGenArchive(a domain.Archive) gen.Archive {
	return gen.Archive{
		Id:        string(a.ID),
		Name:      a.Name,
		CreatedBy: string(a.CreatedBy),
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}

func toGenMember(m domain.Member) gen.Member {
	return gen.Member{
		AccountId:   string(m.AccountID),
		DisplayName: optional(m.DisplayName),
		Role:        gen.Role(m.Role),
		JoinedAt:    m.JoinedAt,
	}
}

func toGenItemType(t *itemtypes.Type) gen.ItemType {
	slots := make([]gen.ItemTypeSlot, 0, len(t.Slots))
	for _, slot := range t.Slots {
		accepts := make([]gen.MediaFamily, 0, len(slot.Accepts))
		for _, a := range slot.Accepts {
			accepts = append(accepts, gen.MediaFamily(a))
		}

		out := gen.ItemTypeSlot{
			Id:      slot.ID,
			Label:   slot.Label,
			Accepts: accepts,
			Min:     slot.Min,
			Max:     slot.Max,
			Ordered: slot.Ordered,
		}
		if slot.MaxSizeBytes > 0 {
			out.MaxSizeBytes = ptr(slot.MaxSizeBytes)
		}
		if slot.Deprecated {
			out.Deprecated = ptr(true)
		}
		if len(slot.RenamedFrom) > 0 {
			out.RenamedFrom = ptr(slot.RenamedFrom)
		}
		slots = append(slots, out)
	}

	presentation := make([]gen.ItemTypePresentation, 0, len(t.Presentation))
	for _, p := range t.Presentation {
		presentation = append(presentation, gen.ItemTypePresentation{
			Primitive: gen.ItemTypePresentationPrimitive(p.Primitive),
			Slots:     p.Slots,
		})
	}

	out := gen.ItemType{
		Id:           t.ID,
		Label:        t.Label,
		Version:      t.Version,
		Icon:         optional(t.Icon),
		Category:     optional(t.Category),
		Slots:        slots,
		Presentation: presentation,
	}
	out.Cover.Prefer = t.Cover.Prefer

	if len(t.Attributes) > 0 {
		attrs := make([]gen.ItemTypeAttribute, 0, len(t.Attributes))
		for _, a := range t.Attributes {
			attr := gen.ItemTypeAttribute{
				Id:    a.ID,
				Label: a.Label,
				Type:  gen.ItemTypeAttributeType(a.Kind),
				Min:   a.Min,
				Max:   a.Max,
			}
			if len(a.Options) > 0 {
				attr.Options = ptr(a.Options)
			}
			attrs = append(attrs, attr)
		}
		out.Attributes = ptr(attrs)
	}

	return out
}
