package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RelType names a kind of edge between two subjects.
type RelType string

const (
	// RelParent goes from parent to child. This is the only edge that forms
	// descent, and the only primitive the tree is built from.
	//
	// There is deliberately no "family" or "couple" record. A family unit
	// breaks the moment anyone remarries, has a half-sibling, has one known
	// parent, or - for animals - sires litters with several dams, which is
	// the normal case rather than the exception. With parent edges as the
	// primitive, siblings, half-siblings and co-parents are all derived and
	// none of those situations needs special handling.
	RelParent RelType = "parent"

	// RelPartner is symmetric and entirely independent of RelParent. People
	// marry and have no children; people have children and never marry.
	// Conflating the two is the family-unit mistake wearing a different hat.
	RelPartner RelType = "partner"

	// RelOwner goes from a person to an animal. It is the one edge that
	// deliberately crosses kinds, and it is what gives "our dog" somewhere
	// to live without making the dog a descendant.
	RelOwner RelType = "owner"

	// RelLittermate is symmetric, between animals.
	RelLittermate RelType = "littermate"
)

// relRule describes what an edge type connects and how it behaves.
type relRule struct {
	// Inverse is what this edge is called read the other way. Stored once
	// and derived on read, so the two directions can never disagree.
	Inverse RelType

	Symmetric bool

	// SameKind requires both ends to be the same kind of subject.
	//
	// This is how "people and animals cannot have shared descendants" is
	// enforced. A child has exactly one kind, so if every parent edge matches
	// it, a shared descendant is not merely discouraged - it cannot be
	// expressed. Stating it as a property rather than as a list of permitted
	// pairs means adding horses or cattle later needs no change here.
	SameKind bool

	// FromKinds and ToKinds restrict a cross-kind edge. Empty means any.
	FromKinds []string
	ToKinds   []string

	// Hierarchical marks an edge that forms a descent graph, which must stay
	// acyclic - nobody can be their own ancestor.
	Hierarchical bool
}

var relRules = map[RelType]relRule{
	RelParent:     {Inverse: "child", SameKind: true, Hierarchical: true},
	RelPartner:    {Inverse: RelPartner, Symmetric: true, SameKind: true},
	RelOwner:      {Inverse: "pet", FromKinds: []string{"person"}, ToKinds: []string{"pet"}},
	RelLittermate: {Inverse: RelLittermate, Symmetric: true, FromKinds: []string{"pet"}, ToKinds: []string{"pet"}},
}

// RelTypes lists every edge type, ordered, for the catalogue endpoint.
func RelTypes() []RelType {
	out := make([]RelType, 0, len(relRules))
	for t := range relRules {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// InverseOf returns what an edge is called read backwards.
func InverseOf(t RelType) (RelType, bool) {
	rule, ok := relRules[t]
	if !ok {
		return "", false
	}
	return rule.Inverse, true
}

// Relationship is one edge.
//
// Deliberately lean: no role on a parent edge, because biological versus
// adoptive is not a distinction this archive draws yet. Adding an optional
// property to an edge later is purely additive - unlike a key schema or an
// index projection, which is why those got settled first and this did not.
type Relationship struct {
	ID        RelID     `json:"id" dynamodbav:"-"`
	ArchiveID ArchiveID `json:"archiveId" dynamodbav:"-"`

	Type   RelType   `json:"type" dynamodbav:"type"`
	FromID SubjectID `json:"fromSubjectId" dynamodbav:"fromSubjectId"`
	ToID   SubjectID `json:"toSubjectId" dynamodbav:"toSubjectId"`

	// Start and End bound the relationship in time. A dog passing from a
	// grandparent to a grandchild, or a marriage that ended, are both things
	// an archive exists to record.
	Start *ArchiveDate `json:"start,omitempty" dynamodbav:"start,omitempty"`
	End   *ArchiveDate `json:"end,omitempty" dynamodbav:"end,omitempty"`

	Notes string `json:"notes,omitempty" dynamodbav:"notes,omitempty"`

	CreatedAt time.Time `json:"createdAt" dynamodbav:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" dynamodbav:"updatedAt"`
	Version   int64     `json:"-" dynamodbav:"version"`
}

// ValidateRelationship checks one edge against the vocabulary and against the
// subjects it names.
func ValidateRelationship(rel *Relationship, from, to *Subject) error {
	var errs ValidationErrors

	rule, ok := relRules[rel.Type]
	if !ok {
		return ValidationErrors{{
			Field: "type", Code: "unknown_type",
			Message: fmt.Sprintf("%q is not a known relationship type", rel.Type),
		}}
	}

	if rel.FromID == rel.ToID {
		errs = append(errs, FieldError{
			Field: "toSubjectId", Code: "self_reference",
			Message: "a subject cannot be related to itself",
		})
	}

	if rule.SameKind && from.Kind != to.Kind {
		errs = append(errs, FieldError{
			Field: "toSubjectId", Code: "kind_mismatch",
			Message: fmt.Sprintf(
				"a %q relationship must join two subjects of the same kind, but these are %q and %q",
				rel.Type, from.Kind, to.Kind),
		})
	}

	if !kindAllowed(rule.FromKinds, from.Kind) {
		errs = append(errs, FieldError{
			Field: "fromSubjectId", Code: "kind_not_allowed",
			Message: fmt.Sprintf("a %q relationship cannot start at a %q", rel.Type, from.Kind),
		})
	}
	if !kindAllowed(rule.ToKinds, to.Kind) {
		errs = append(errs, FieldError{
			Field: "toSubjectId", Code: "kind_not_allowed",
			Message: fmt.Sprintf("a %q relationship cannot end at a %q", rel.Type, to.Kind),
		})
	}

	for field, date := range map[string]*ArchiveDate{"start": rel.Start, "end": rel.End} {
		if date == nil {
			continue
		}
		if err := date.Validate(); err != nil {
			errs = append(errs, FieldError{Field: field, Code: "invalid", Message: err.Error()})
		}
	}

	return errs.OrNil()
}

func kindAllowed(allowed []string, kind string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, k := range allowed {
		if k == kind {
			return true
		}
	}
	return false
}

// Graph is every subject and edge in an archive, indexed for traversal.
//
// Built in memory from two queries. Because the whole edge set is read
// anyway, traversal direction costs nothing - which is why edges are stored
// once in their canonical direction rather than dual-written with inverses
// that could drift apart.
type Graph struct {
	Subjects map[SubjectID]*Subject

	parents  map[SubjectID][]SubjectID
	children map[SubjectID][]SubjectID
	partners map[SubjectID][]SubjectID
	pets     map[SubjectID][]SubjectID
	owners   map[SubjectID][]SubjectID

	edges []*Relationship
}

func NewGraph(subjects []*Subject, relationships []*Relationship) *Graph {
	g := &Graph{
		Subjects: make(map[SubjectID]*Subject, len(subjects)),
		parents:  map[SubjectID][]SubjectID{},
		children: map[SubjectID][]SubjectID{},
		partners: map[SubjectID][]SubjectID{},
		pets:     map[SubjectID][]SubjectID{},
		owners:   map[SubjectID][]SubjectID{},
		edges:    relationships,
	}

	for _, s := range subjects {
		g.Subjects[s.ID] = s
	}

	for _, rel := range relationships {
		switch rel.Type {
		case RelParent:
			g.children[rel.FromID] = append(g.children[rel.FromID], rel.ToID)
			g.parents[rel.ToID] = append(g.parents[rel.ToID], rel.FromID)
		case RelPartner:
			g.partners[rel.FromID] = append(g.partners[rel.FromID], rel.ToID)
			g.partners[rel.ToID] = append(g.partners[rel.ToID], rel.FromID)
		case RelOwner:
			g.pets[rel.FromID] = append(g.pets[rel.FromID], rel.ToID)
			g.owners[rel.ToID] = append(g.owners[rel.ToID], rel.FromID)
		}
	}
	return g
}

// WouldCycle reports whether making parent a parent of child would make
// someone their own ancestor.
//
// Worth checking on every write. Without it a single mistaken edit produces a
// loop that hangs tree rendering, and the cause is almost impossible to spot
// afterwards - the data looks fine one edge at a time.
func (g *Graph) WouldCycle(parent, child SubjectID) bool {
	if parent == child {
		return true
	}

	// Is the proposed parent already somewhere below the child?
	seen := map[SubjectID]bool{child: true}
	queue := []SubjectID{child}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, descendant := range g.children[current] {
			if descendant == parent {
				return true
			}
			if !seen[descendant] {
				seen[descendant] = true
				queue = append(queue, descendant)
			}
		}
	}
	return false
}

// TreeDirection says which way a tree walk runs.
type TreeDirection string

const (
	TreeAncestors   TreeDirection = "ancestors"
	TreeDescendants TreeDirection = "descendants"
	TreeBoth        TreeDirection = "both"
)

// Tree is the response shape: a flat graph, never nesting.
//
// Nesting cannot represent a subject that appears more than once, and that is
// ordinary rather than exotic - line-breeding puts the same animal on both
// sides of a pedigree constantly, and cousin marriage does the same in human
// genealogy. Nesting would also duplicate a subject's whole record once per
// appearance. The client lays this out.
type Tree struct {
	RootID SubjectID       `json:"rootId"`
	Nodes  []*Subject      `json:"nodes"`
	Edges  []*Relationship `json:"edges"`
}

// BuildTree walks the graph from root.
//
// Depth counts generations along parent edges only. Partners and animals are
// attached to each visited subject as leaves - one hop, no depth consumed,
// and never traversed through.
//
// That rule matters more than it looks. If ownership were followed
// recursively, asking for someone's descendants would return their dog's
// puppies, grafting a pedigree into a family tree and making "depth" mean two
// different things in the same response. A dog hangs off its owner; ask for a
// tree rooted at the dog to see the dog's own line.
func (g *Graph) BuildTree(root SubjectID, direction TreeDirection, depth int) (*Tree, error) {
	if _, ok := g.Subjects[root]; !ok {
		return nil, fmt.Errorf("subject %q is not in this archive", root)
	}
	if depth < 0 {
		depth = 0
	}

	included := map[SubjectID]bool{root: true}

	type step struct {
		id    SubjectID
		depth int
	}
	queue := []step{{id: root}}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current.depth >= depth {
			continue
		}

		var next []SubjectID
		if direction == TreeAncestors || direction == TreeBoth {
			next = append(next, g.parents[current.id]...)
		}
		if direction == TreeDescendants || direction == TreeBoth {
			next = append(next, g.children[current.id]...)
		}

		for _, id := range next {
			if included[id] {
				continue
			}
			included[id] = true
			queue = append(queue, step{id: id, depth: current.depth + 1})
		}
	}

	// Attach partners and animals to everyone reached, without expanding
	// through them.
	for id := range snapshot(included) {
		for _, partner := range g.partners[id] {
			included[partner] = true
		}
		for _, pet := range g.pets[id] {
			included[pet] = true
		}
		for _, owner := range g.owners[id] {
			included[owner] = true
		}
	}

	tree := &Tree{RootID: root}

	ids := make([]SubjectID, 0, len(included))
	for id := range included {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		if subject, ok := g.Subjects[id]; ok {
			tree.Nodes = append(tree.Nodes, subject)
		}
	}

	// Every edge whose both ends made it in, so the client can draw the
	// connections between what it was given and nothing dangling.
	for _, rel := range g.edges {
		if included[rel.FromID] && included[rel.ToID] {
			tree.Edges = append(tree.Edges, rel)
		}
	}

	if tree.Nodes == nil {
		tree.Nodes = []*Subject{}
	}
	if tree.Edges == nil {
		tree.Edges = []*Relationship{}
	}
	return tree, nil
}

// snapshot copies a set so it can be iterated while being added to.
func snapshot(set map[SubjectID]bool) map[SubjectID]bool {
	out := make(map[SubjectID]bool, len(set))
	for k, v := range set {
		out[k] = v
	}
	return out
}

// RelIDFor derives an edge's id from what it connects.
//
// An edge has no identity beyond its endpoints and its type, so deriving the
// id rather than minting one means creating the same relationship twice is
// naturally idempotent, and there is no second lookup path that could
// disagree with the first.
func RelIDFor(from SubjectID, relType RelType, to SubjectID) RelID {
	return RelID(prefixRel + string(from) + "." + string(relType) + "." + string(to))
}

// ParseRelID reverses RelIDFor.
func ParseRelID(id RelID) (from SubjectID, relType RelType, to SubjectID, err error) {
	rest, ok := strings.CutPrefix(string(id), prefixRel)
	if !ok {
		return "", "", "", fmt.Errorf("%q is not a relationship id", id)
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("%q is not a relationship id", id)
	}
	return SubjectID(parts[0]), RelType(parts[1]), SubjectID(parts[2]), nil
}
