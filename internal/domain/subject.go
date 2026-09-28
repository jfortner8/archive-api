package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/jfortner8/archive-api/internal/domain/subjecttypes"
)

// Subject is whoever or whatever a record is about - a person, a pet, and in
// time perhaps a house or a vehicle, which people genuinely tag photographs
// with.
//
// People and pets are one entity with a kind rather than two parallel ones.
// Each kind declares its own attributes, so a pet is never forced into a
// person's shape; what they share is the part that would otherwise multiply
// with every new kind - one tag list on items, one filter, one index
// attribute.
type Subject struct {
	ID        SubjectID `json:"id" dynamodbav:"-"`
	ArchiveID ArchiveID `json:"archiveId" dynamodbav:"-"`

	Kind        string `json:"kind" dynamodbav:"kind"`
	KindVersion int    `json:"kindVersion" dynamodbav:"kindVersion"`

	// DisplayName is what appears on a gallery caption and a tree node. Kept
	// separate from the name parts because an archive is full of subjects
	// whose full name nobody knows - "Aunt Bea", "the collie".
	DisplayName string `json:"displayName" dynamodbav:"displayName"`

	GivenNames string   `json:"givenNames,omitempty" dynamodbav:"givenNames,omitempty"`
	FamilyName string   `json:"familyName,omitempty" dynamodbav:"familyName,omitempty"`
	Aliases    []string `json:"aliases" dynamodbav:"aliases"`

	// Birth and Death reuse ArchiveDate because genealogy dates are exactly
	// as uncertain as item dates - "circa 1890" is the normal case, not an
	// edge case.
	Birth     *ArchiveDate `json:"birth,omitempty" dynamodbav:"birth,omitempty"`
	BirthNorm *Normalized  `json:"birthNormalized,omitempty" dynamodbav:"birthNormalized,omitempty"`
	Death     *ArchiveDate `json:"death,omitempty" dynamodbav:"death,omitempty"`
	DeathNorm *Normalized  `json:"deathNormalized,omitempty" dynamodbav:"deathNormalized,omitempty"`

	Notes string `json:"notes,omitempty" dynamodbav:"notes,omitempty"`

	// Attributes holds what this kind declares - a person's gender, a pet's
	// species and breed.
	//
	// Gender lives here, as a declared attribute, precisely because it is
	// descriptive and never structural: nothing in the family tree reads it.
	// A tree that positioned or ordered people by gender would be encoding an
	// assumption the data does not support.
	Attributes map[string]any `json:"attributes,omitempty" dynamodbav:"attributes,omitempty"`

	// CoverItemID is the portrait. A family tree without faces is a
	// spreadsheet, so a subject carries enough to render one without a join.
	CoverItemID ItemID `json:"coverItemId,omitempty" dynamodbav:"coverItemId,omitempty"`
	CoverKey    string `json:"-" dynamodbav:"coverKey,omitempty"`

	CreatedAt time.Time `json:"createdAt" dynamodbav:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" dynamodbav:"updatedAt"`
	Version   int64     `json:"-" dynamodbav:"version"`
}

// Ref is how this subject is denormalized onto an item.
func (s *Subject) Ref() SubjectRef {
	return SubjectRef{ID: s.ID, Kind: s.Kind, Name: s.DisplayName}
}

// Kind resolves this subject's entry in the registry.
func (s *Subject) KindDef(reg *subjecttypes.Registry) (*subjecttypes.Kind, bool) {
	return reg.Get(s.Kind)
}

// Validate checks a subject against its declared kind.
func (s *Subject) Validate(reg *subjecttypes.Registry) error {
	var errs ValidationErrors

	kind, ok := reg.Get(s.Kind)
	if !ok {
		return ValidationErrors{{
			Field: "kind", Code: "unknown_kind",
			Message: fmt.Sprintf("%q is not a known subject kind", s.Kind),
		}}
	}

	if strings.TrimSpace(s.DisplayName) == "" {
		errs = append(errs, FieldError{
			Field: "displayName", Code: "required",
			Message: "displayName is required",
		})
	}

	for field, date := range map[string]*ArchiveDate{"birth": s.Birth, "death": s.Death} {
		if date == nil {
			continue
		}
		if err := date.Validate(); err != nil {
			errs = append(errs, FieldError{Field: field, Code: "invalid", Message: err.Error()})
		}
	}

	// Someone cannot have died before they were born. Worth checking because
	// it is usually a transposed pair of dates during data entry, and it is
	// invisible afterwards.
	if s.Birth != nil && s.Death != nil {
		birth, errB := s.Birth.Normalize()
		death, errD := s.Death.Normalize()
		if errB == nil && errD == nil && death.Latest < birth.Earliest {
			errs = append(errs, FieldError{
				Field: "death", Code: "before_birth",
				Message: "the death date is entirely before the birth date",
			})
		}
	}

	errs = append(errs, ValidateAttributes(kind.ID, kind.Attributes, s.Attributes)...)
	return errs.OrNil()
}

// Normalize recomputes the derived date fields.
func (s *Subject) Normalize(reg *subjecttypes.Registry) error {
	for _, pair := range []struct {
		date **ArchiveDate
		norm **Normalized
	}{
		{&s.Birth, &s.BirthNorm},
		{&s.Death, &s.DeathNorm},
	} {
		if *pair.date == nil {
			*pair.norm = nil
			continue
		}
		(*pair.date).Resolve()
		n, err := (*pair.date).Normalize()
		if err != nil {
			return err
		}
		*pair.norm = &n
	}

	if s.Aliases == nil {
		s.Aliases = []string{}
	}
	if kind, ok := reg.Get(s.Kind); ok {
		s.KindVersion = kind.Version
	}
	return nil
}

// SubjectPatch is a partial update. Same absent-versus-null distinction as
// ItemPatch.
type SubjectPatch struct {
	DisplayName Opt[string]
	GivenNames  Opt[string]
	FamilyName  Opt[string]
	Aliases     Opt[[]string]
	Birth       Opt[ArchiveDate]
	Death       Opt[ArchiveDate]
	Notes       Opt[string]
	Attributes  Opt[map[string]any]
	CoverItemID Opt[ItemID]
}

func (p SubjectPatch) Apply(s *Subject) {
	if v, ok := p.DisplayName.Get(); ok {
		s.DisplayName = v
	}
	if p.GivenNames.Set {
		v, _ := p.GivenNames.Get()
		s.GivenNames = v
	}
	if p.FamilyName.Set {
		v, _ := p.FamilyName.Get()
		s.FamilyName = v
	}
	if p.Aliases.Set {
		v, _ := p.Aliases.Get()
		s.Aliases = v
	}
	if p.Notes.Set {
		v, _ := p.Notes.Get()
		s.Notes = v
	}
	if p.Attributes.Set {
		v, _ := p.Attributes.Get()
		s.Attributes = v
	}

	switch {
	case p.Birth.Present():
		v, _ := p.Birth.Get()
		s.Birth = &v
	case p.Birth.Clears():
		s.Birth = nil
	}

	switch {
	case p.Death.Present():
		v, _ := p.Death.Get()
		s.Death = &v
	case p.Death.Clears():
		s.Death = nil
	}

	switch {
	case p.CoverItemID.Present():
		v, _ := p.CoverItemID.Get()
		s.CoverItemID = v
	case p.CoverItemID.Clears():
		s.CoverItemID = ""
	}
}
