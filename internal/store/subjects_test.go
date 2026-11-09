package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jfortner8/archive-api/internal/domain"
)

func newSubject(kind, name string) *domain.Subject {
	return &domain.Subject{
		ID:          domain.NewSubjectID(),
		Kind:        kind,
		DisplayName: name,
	}
}

func mustCreateSubject(t *testing.T, s *Store, archive domain.ArchiveID, subject *domain.Subject) *domain.Subject {
	t.Helper()
	subject.ArchiveID = archive
	if err := s.CreateSubject(context.Background(), subject); err != nil {
		t.Fatalf("create subject %q: %v", subject.DisplayName, err)
	}
	return subject
}

func TestSubjectRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	grandma := newSubject("person", "Grandma")
	grandma.Birth = &domain.ArchiveDate{Kind: domain.DateCirca, Date: "1912"}
	grandma.Attributes = map[string]any{"occupation": "Schoolteacher"}
	mustCreateSubject(t, s, archive, grandma)

	got, err := s.GetSubject(ctx, archive, grandma.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "Grandma" || got.Kind != "person" {
		t.Errorf("subject = %q/%q", got.Kind, got.DisplayName)
	}
	// Genealogy dates are as uncertain as item dates, and get the same
	// treatment - including the circa width being inferred and stored.
	if got.Birth == nil || got.Birth.Unit == "" {
		t.Error("a circa birth date was not resolved on write")
	}
	if got.BirthNorm == nil || got.BirthNorm.Sort == "" {
		t.Error("the birth date was not normalized")
	}
}

func TestSubjectsAreScopedToTheirArchive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mine, theirs := domain.NewArchiveID(), domain.NewArchiveID()
	subject := mustCreateSubject(t, s, mine, newSubject("person", "Private"))

	if _, err := s.GetSubject(ctx, theirs, subject.ID); err != ErrNotFound {
		t.Errorf("cross-archive get = %v, want ErrNotFound", err)
	}
	others, err := s.ListSubjects(ctx, theirs, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(others) != 0 {
		t.Errorf("another archive's list returned %d subjects", len(others))
	}
}

func TestListSubjectsByKind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	mustCreateSubject(t, s, archive, newSubject("person", "Grandma"))
	mustCreateSubject(t, s, archive, newSubject("person", "Mother"))
	mustCreateSubject(t, s, archive, newSubject("pet", "Rufus"))

	all, err := s.ListSubjects(ctx, archive, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %d subjects (%v), want 3", len(all), err)
	}

	people, _ := s.ListSubjects(ctx, archive, "person")
	if len(people) != 2 {
		t.Errorf("people = %d, want 2", len(people))
	}
	pets, _ := s.ListSubjects(ctx, archive, "pet")
	if len(pets) != 1 || pets[0].DisplayName != "Rufus" {
		t.Errorf("pets = %v, want just Rufus", pets)
	}
}

// TestRelationshipKindRulesAreEnforcedOnWrite is the same-kind constraint
// reaching the database, not just the pure validator.
func TestRelationshipKindRulesAreEnforcedOnWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	grandma := mustCreateSubject(t, s, archive, newSubject("person", "Grandma"))
	mother := mustCreateSubject(t, s, archive, newSubject("person", "Mother"))
	rufus := mustCreateSubject(t, s, archive, newSubject("pet", "Rufus"))
	puppy := mustCreateSubject(t, s, archive, newSubject("pet", "Puppy"))

	put := func(relType domain.RelType, from, to *domain.Subject) error {
		return s.PutRelationship(ctx, &domain.Relationship{
			ArchiveID: archive, Type: relType, FromID: from.ID, ToID: to.ID,
		})
	}

	if err := put(domain.RelParent, grandma, mother); err != nil {
		t.Errorf("person->person parent rejected: %v", err)
	}
	if err := put(domain.RelParent, rufus, puppy); err != nil {
		t.Errorf("pet->pet parent rejected: %v", err)
	}
	if err := put(domain.RelParent, grandma, rufus); err == nil {
		t.Error("a person was stored as the parent of an animal")
	}
	if err := put(domain.RelOwner, grandma, rufus); err != nil {
		t.Errorf("person->pet ownership rejected: %v", err)
	}
	if err := put(domain.RelOwner, rufus, grandma); err == nil {
		t.Error("an animal was stored as the owner of a person")
	}

	// An edge naming a subject that isn't in this archive is a miss, not a
	// dangling row.
	if err := put(domain.RelParent, grandma, &domain.Subject{ID: domain.NewSubjectID()}); err != ErrNotFound {
		t.Errorf("edge to an unknown subject = %v, want ErrNotFound", err)
	}
}

// TestCycleIsRejectedOnWrite covers the check that needs the existing graph,
// so it can only happen at the store boundary.
func TestCycleIsRejectedOnWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	a := mustCreateSubject(t, s, archive, newSubject("person", "A"))
	b := mustCreateSubject(t, s, archive, newSubject("person", "B"))
	c := mustCreateSubject(t, s, archive, newSubject("person", "C"))

	for _, pair := range [][2]*domain.Subject{{a, b}, {b, c}} {
		if err := s.PutRelationship(ctx, &domain.Relationship{
			ArchiveID: archive, Type: domain.RelParent, FromID: pair[0].ID, ToID: pair[1].ID,
		}); err != nil {
			t.Fatalf("building the chain: %v", err)
		}
	}

	err := s.PutRelationship(ctx, &domain.Relationship{
		ArchiveID: archive, Type: domain.RelParent, FromID: c.ID, ToID: a.ID,
	})
	if err == nil {
		t.Fatal("a grandchild was stored as their grandparent's parent")
	}
	if !strings.Contains(err.Error(), "own ancestor") {
		t.Errorf("error = %q, want it to explain the cycle", err)
	}
}

// TestRelationshipsAreIdempotent: the edge's identity is its endpoints and
// type, so recording the same fact twice records it once.
func TestRelationshipsAreIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	parent := mustCreateSubject(t, s, archive, newSubject("person", "Parent"))
	child := mustCreateSubject(t, s, archive, newSubject("person", "Child"))

	for i := 0; i < 2; i++ {
		if err := s.PutRelationship(ctx, &domain.Relationship{
			ArchiveID: archive, Type: domain.RelParent, FromID: parent.ID, ToID: child.ID,
		}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	edges, err := s.ListRelationships(ctx, archive)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("edge count = %d, want 1", len(edges))
	}
}

// TestDeletingASubjectRemovesItsEdges keeps the graph an invariant, so a
// tree walk never has to defend against a missing node.
func TestDeletingASubjectRemovesItsEdges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	grandma := mustCreateSubject(t, s, archive, newSubject("person", "Grandma"))
	mother := mustCreateSubject(t, s, archive, newSubject("person", "Mother"))
	rufus := mustCreateSubject(t, s, archive, newSubject("pet", "Rufus"))

	for _, rel := range []*domain.Relationship{
		{ArchiveID: archive, Type: domain.RelParent, FromID: grandma.ID, ToID: mother.ID},
		{ArchiveID: archive, Type: domain.RelOwner, FromID: grandma.ID, ToID: rufus.ID},
	} {
		if err := s.PutRelationship(ctx, rel); err != nil {
			t.Fatalf("put: %v", err)
		}
	}

	if err := s.DeleteSubject(ctx, archive, grandma.ID, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}

	edges, err := s.ListRelationships(ctx, archive)
	if err != nil {
		t.Fatalf("list relationships: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("%d edges survived the deletion of the subject they name", len(edges))
	}

	// The other subjects are untouched.
	if _, err := s.GetSubject(ctx, archive, mother.ID); err != nil {
		t.Errorf("an unrelated subject was removed: %v", err)
	}
	if _, err := s.GetSubject(ctx, archive, rufus.ID); err != nil {
		t.Errorf("the animal was removed along with its owner: %v", err)
	}
}

// TestLoadGraphAndWalk is the end-to-end shape of the feature: two queries,
// then a tree.
func TestLoadGraphAndWalk(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	archive := domain.NewArchiveID()

	grandma := mustCreateSubject(t, s, archive, newSubject("person", "Grandma"))
	mother := mustCreateSubject(t, s, archive, newSubject("person", "Mother"))
	child := mustCreateSubject(t, s, archive, newSubject("person", "Child"))
	rufus := mustCreateSubject(t, s, archive, newSubject("pet", "Rufus"))
	puppy := mustCreateSubject(t, s, archive, newSubject("pet", "Puppy"))

	for _, rel := range []*domain.Relationship{
		{ArchiveID: archive, Type: domain.RelParent, FromID: grandma.ID, ToID: mother.ID},
		{ArchiveID: archive, Type: domain.RelParent, FromID: mother.ID, ToID: child.ID},
		{ArchiveID: archive, Type: domain.RelOwner, FromID: mother.ID, ToID: rufus.ID},
		{ArchiveID: archive, Type: domain.RelParent, FromID: rufus.ID, ToID: puppy.ID},
	} {
		if err := s.PutRelationship(ctx, rel); err != nil {
			t.Fatalf("put: %v", err)
		}
	}

	graph, err := s.LoadGraph(ctx, archive)
	if err != nil {
		t.Fatalf("load graph: %v", err)
	}

	tree, err := graph.BuildTree(grandma.ID, domain.TreeDescendants, 2)
	if err != nil {
		t.Fatalf("build tree: %v", err)
	}

	in := map[domain.SubjectID]bool{}
	for _, node := range tree.Nodes {
		in[node.ID] = true
	}

	for _, want := range []*domain.Subject{mother, child, rufus} {
		if !in[want.ID] {
			t.Errorf("%q missing from the tree", want.DisplayName)
		}
	}
	// The dog's own offspring stay out: ownership is one hop, never a path
	// the walk continues along.
	if in[puppy.ID] {
		t.Error("the walk continued through an ownership edge into the dog's pedigree")
	}
}
