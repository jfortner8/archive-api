package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
)

// FieldError names which field was wrong, so the API can return field-level
// detail instead of one opaque sentence. A capture form with eight inputs
// needs to know which one to highlight.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// ValidationErrors is every problem found, rather than only the first. One
// round trip per mistake is a miserable way to fill in a form.
type ValidationErrors []FieldError

func (v ValidationErrors) Error() string {
	parts := make([]string, 0, len(v))
	for _, e := range v {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

func (v ValidationErrors) OrNil() error {
	if len(v) == 0 {
		return nil
	}
	return v
}

// Validate checks an item against the registry: that its type exists, that
// every file sits in a slot that type declares, that slot cardinality holds,
// that each file's media family is one the slot accepts, and that attributes
// match what the type declares.
//
// All of this used to be unchecked. `type` and `role` were bare strings, so
// nothing stopped a "disc" file on a document, or six fronts on a two-sided
// photo.
func (i *Item) Validate(reg *itemtypes.Registry) error {
	var errs ValidationErrors

	typ, ok := reg.Get(i.TypeID)
	if !ok {
		return ValidationErrors{{
			Field:   "typeId",
			Code:    "unknown_type",
			Message: fmt.Sprintf("%q is not a known item type", i.TypeID),
		}}
	}

	if strings.TrimSpace(i.Title) == "" {
		errs = append(errs, FieldError{Field: "title", Code: "required", Message: "title is required"})
	}

	if i.Date != nil {
		if err := i.Date.Validate(); err != nil {
			errs = append(errs, FieldError{Field: "date", Code: "invalid", Message: err.Error()})
		}
	}

	if err := i.Location.Validate(); err != nil {
		errs = append(errs, FieldError{Field: "location", Code: "invalid", Message: err.Error()})
	}

	errs = append(errs, validateFiles(typ, i.Files)...)
	errs = append(errs, validateAttributes(typ, i.Attributes)...)
	errs = append(errs, validateSubjects(i.Subjects)...)

	return errs.OrNil()
}

func validateFiles(typ *itemtypes.Type, files []File) ValidationErrors {
	var errs ValidationErrors
	counts := map[string]int{}

	for idx, f := range files {
		field := fmt.Sprintf("files[%d]", idx)

		slot, ok := typ.Slot(f.Role)
		if !ok {
			errs = append(errs, FieldError{
				Field:   field + ".role",
				Code:    "unknown_slot",
				Message: fmt.Sprintf("%q declares no slot %q", typ.ID, f.Role),
			})
			continue
		}

		// Count against the canonical id, so files stored under a renamed
		// slot's old id still count toward the right limit.
		counts[slot.ID]++

		fam, err := itemtypes.FamilyForContentType(f.ContentType)
		if err != nil {
			errs = append(errs, FieldError{
				Field: field + ".contentType", Code: "unsupported_media", Message: err.Error(),
			})
			continue
		}
		if !slot.AcceptsFamily(fam) {
			errs = append(errs, FieldError{
				Field: field + ".contentType", Code: "wrong_media_family",
				Message: fmt.Sprintf("slot %q accepts %v, not %q", slot.ID, slot.Accepts, fam),
			})
		}
		if slot.MaxSizeBytes > 0 && f.SizeBytes > slot.MaxSizeBytes {
			errs = append(errs, FieldError{
				Field: field + ".sizeBytes", Code: "too_large",
				Message: fmt.Sprintf("slot %q allows at most %d bytes", slot.ID, slot.MaxSizeBytes),
			})
		}
	}

	// Sorted so the same bad request always produces the same response, which
	// makes it testable and makes a diff of two failures readable.
	slotIDs := make([]string, 0, len(typ.Slots))
	for idx := range typ.Slots {
		slotIDs = append(slotIDs, typ.Slots[idx].ID)
	}
	sort.Strings(slotIDs)

	for _, id := range slotIDs {
		slot, _ := typ.Slot(id)
		n := counts[id]

		// Min is almost always 0. An archive is full of partial records - a
		// CD with only an MP3, a photo whose back was never scanned - and
		// refusing to store them would be refusing the actual job.
		if n < slot.Min {
			errs = append(errs, FieldError{
				Field: "files", Code: "slot_underfilled",
				Message: fmt.Sprintf("slot %q needs at least %d file(s), got %d", id, slot.Min, n),
			})
		}
		if n > slot.Max {
			errs = append(errs, FieldError{
				Field: "files", Code: "slot_overfilled",
				Message: fmt.Sprintf("slot %q holds at most %d file(s), got %d", id, slot.Max, n),
			})
		}
	}

	return errs
}

func validateAttributes(typ *itemtypes.Type, attrs map[string]any) ValidationErrors {
	var errs ValidationErrors

	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		field := "attributes." + key

		decl, ok := typ.Attribute(key)
		if !ok {
			errs = append(errs, FieldError{
				Field: field, Code: "unknown_attribute",
				Message: fmt.Sprintf("%q declares no attribute %q", typ.ID, key),
			})
			continue
		}

		if err := checkAttrValue(decl, attrs[key]); err != nil {
			errs = append(errs, FieldError{Field: field, Code: "invalid", Message: err.Error()})
		}
	}

	return errs
}

// checkAttrValue type-checks one attribute. Values arrive from JSON, so every
// number is a float64 regardless of how it was written.
func checkAttrValue(decl *itemtypes.Attribute, value any) error {
	switch decl.Kind {
	case itemtypes.AttrString:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected a string")
		}

	case itemtypes.AttrEnum:
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected a string")
		}
		for _, opt := range decl.Options {
			if opt == s {
				return nil
			}
		}
		return fmt.Errorf("expected one of %v", decl.Options)

	case itemtypes.AttrBool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected true or false")
		}

	case itemtypes.AttrInteger, itemtypes.AttrNumber:
		n, ok := toFloat(value)
		if !ok {
			return fmt.Errorf("expected a number")
		}
		if decl.Kind == itemtypes.AttrInteger && n != float64(int64(n)) {
			return fmt.Errorf("expected a whole number")
		}
		if decl.Min != nil && n < *decl.Min {
			return fmt.Errorf("must be at least %v", *decl.Min)
		}
		if decl.Max != nil && n > *decl.Max {
			return fmt.Errorf("must be at most %v", *decl.Max)
		}
	}

	return nil
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func validateSubjects(subjects []SubjectRef) ValidationErrors {
	var errs ValidationErrors
	seen := map[SubjectID]bool{}

	for idx, s := range subjects {
		field := fmt.Sprintf("subjects[%d]", idx)

		if s.ID == "" {
			errs = append(errs, FieldError{Field: field + ".id", Code: "required", Message: "subject id is required"})
			continue
		}
		if seen[s.ID] {
			errs = append(errs, FieldError{
				Field: field + ".id", Code: "duplicate",
				Message: fmt.Sprintf("subject %q is tagged twice", s.ID),
			})
		}
		seen[s.ID] = true
	}

	return errs
}
