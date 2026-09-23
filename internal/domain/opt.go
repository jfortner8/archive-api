package domain

// Opt distinguishes three states a field can be in during a partial update:
// absent from the request, present with a value, and present but explicitly
// null.
//
// The old PATCH handler used plain pointers, which collapses the last two:
// a nil pointer meant "not mentioned", so there was no way to express
// "remove this item's date". Clearing a field simply wasn't possible.
//
//	Opt[string]{}                              -> leave alone
//	Opt[string]{Set: true, Value: &s}          -> set to s
//	Opt[string]{Set: true}                     -> clear
type Opt[T any] struct {
	Set   bool
	Value *T
}

// Present reports whether a value was supplied (as opposed to absent, or
// explicitly null).
func (o Opt[T]) Present() bool { return o.Set && o.Value != nil }

// Clears reports whether this asks for the field to be removed.
func (o Opt[T]) Clears() bool { return o.Set && o.Value == nil }

// Get returns the supplied value and whether there was one.
func (o Opt[T]) Get() (T, bool) {
	if !o.Present() {
		var zero T
		return zero, false
	}
	return *o.Value, true
}

// SetTo builds an Opt that assigns a value.
func SetTo[T any](v T) Opt[T] { return Opt[T]{Set: true, Value: &v} }

// Clear builds an Opt that removes a field.
func Clear[T any]() Opt[T] { return Opt[T]{Set: true} }

// ItemPatch is a partial update to an item's metadata. Fields left zero are
// untouched.
//
// Files are deliberately absent: they have their own operations (append,
// delete, reorder) because each has different concurrency and storage
// consequences, and folding them into a metadata patch would hide that.
type ItemPatch struct {
	Title       Opt[string]
	Date        Opt[ArchiveDate]
	Location    Opt[Location]
	PlaceID     Opt[PlaceID]
	Notes       Opt[string]
	Tags        Opt[[]string]
	Subjects    Opt[[]SubjectRef]
	Attributes  Opt[map[string]any]
	CoverFileID Opt[FileID]
}

// Apply mutates an item in place. Call Normalize afterwards to refresh the
// derived fields; the store does that for you.
func (p ItemPatch) Apply(item *Item) {
	if v, ok := p.Title.Get(); ok {
		item.Title = v
	}

	switch {
	case p.Date.Present():
		v, _ := p.Date.Get()
		item.Date = &v
	case p.Date.Clears():
		item.Date = nil
	}

	switch {
	case p.Location.Present():
		v, _ := p.Location.Get()
		item.Location = &v
	case p.Location.Clears():
		item.Location = nil
	}

	switch {
	case p.PlaceID.Present():
		v, _ := p.PlaceID.Get()
		item.PlaceID = v
	case p.PlaceID.Clears():
		item.PlaceID = ""
	}

	if p.Notes.Set {
		v, _ := p.Notes.Get()
		item.Notes = v
	}
	if p.Tags.Set {
		v, _ := p.Tags.Get()
		item.Tags = v
	}
	if p.Subjects.Set {
		v, _ := p.Subjects.Get()
		item.Subjects = v
	}
	if p.Attributes.Set {
		v, _ := p.Attributes.Get()
		item.Attributes = v
	}
	// Setting a cover pins it; clearing it hands the choice back to the
	// server, which will derive one from the type's preference order.
	switch {
	case p.CoverFileID.Present():
		v, _ := p.CoverFileID.Get()
		item.CoverFileID = v
		item.CoverPinned = true
	case p.CoverFileID.Clears():
		item.CoverFileID = ""
		item.CoverPinned = false
	}
}
