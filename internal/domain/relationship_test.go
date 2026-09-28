package domain

import (
	"strings"
	"testing"
)

func person(id, name string) *Subject {
	return &Subject{ID: SubjectID(id), Kind: "person", DisplayName: name}
}

func pet(id, name string) *Subject {
	return &Subject{ID: SubjectID(id), Kind: "pet", DisplayName: name}
}

func edge(t RelType, from, to *Subject) *Relationship {
	return &Relationship{
		ID:     RelID("rel_" + string(from.ID) + string(to.ID) + string(t)),
		Type:   t,
		FromID: from.ID,
		ToID:   to.ID,
	}
}

// TestParentEdgesCannotCrossKinds is the rule that makes shared descendants
// between people and animals unrepresentable rather than merely discouraged.
func TestParentEdgesCannotCrossKinds(t *testing.T) {
	grandma := person("s_grandma", "Grandma")
	mother := person("s_mother", "Mother")
	rufus := pet("s_rufus", "Rufus")
	puppy := pet("s_puppy", "Puppy")

	t.Run("person to person is fine", func(t *testing.T) {
		if err := ValidateRelationship(edge(RelParent, grandma, mother), grandma, mother); err != nil {
			t.Errorf("rejected a valid parent edge: %v", err)
		}
	})

	t.Run("pet to pet is fine", func(t *testing.T) {
		if err := ValidateRelationship(edge(RelParent, rufus, puppy), rufus, puppy); err != nil {
			t.Errorf("rejected a valid pedigree edge: %v", err)
		}
	})

	t.Run("person cannot parent a pet", func(t *testing.T) {
		err := ValidateRelationship(edge(RelParent, grandma, rufus), grandma, rufus)
		if err == nil {
			t.Fatal("a person was allowed to be the parent of an animal")
		}
		if !strings.Contains(err.Error(), "same kind") {
			t.Errorf("error = %q, want it to explain the same-kind rule", err)
		}
	})

	t.Run("pet cannot parent a person", func(t *testing.T) {
		if err := ValidateRelationship(edge(RelParent, rufus, mother), rufus, mother); err == nil {
			t.Fatal("an animal was allowed to be the parent of a person")
		}
	})
}

// TestOwnershipIsTheOneCrossKindEdge covers the relationship that gives "our
// dog" somewhere to live without making the dog a descendant.
func TestOwnershipIsTheOneCrossKindEdge(t *testing.T) {
	grandma := person("s_grandma", "Grandma")
	mother := person("s_mother", "Mother")
	rufus := pet("s_rufus", "Rufus")
	tibbles := pet("s_tibbles", "Tibbles")

	if err := ValidateRelationship(edge(RelOwner, grandma, rufus), grandma, rufus); err != nil {
		t.Errorf("a person could not own an animal: %v", err)
	}
	if err := ValidateRelationship(edge(RelOwner, rufus, grandma), rufus, grandma); err == nil {
		t.Error("an animal was allowed to own a person")
	}
	if err := ValidateRelationship(edge(RelOwner, rufus, tibbles), rufus, tibbles); err == nil {
		t.Error("an animal was allowed to own another animal")
	}
	if err := ValidateRelationship(edge(RelOwner, grandma, mother), grandma, mother); err == nil {
		t.Error("a person was allowed to own another person")
	}
}

func TestSelfReferenceIsRejected(t *testing.T) {
	alone := person("s_alone", "Alone")
	if err := ValidateRelationship(edge(RelParent, alone, alone), alone, alone); err == nil {
		t.Error("a subject was allowed to be its own parent")
	}
}

// TestCycleDetection covers the failure that is nearly impossible to
// diagnose after the fact: one bad edit makes someone their own ancestor and
// tree rendering loops forever, while every individual edge looks fine.
func TestCycleDetection(t *testing.T) {
	a := person("s_a", "A")
	b := person("s_b", "B")
	c := person("s_c", "C")

	g := NewGraph(
		[]*Subject{a, b, c},
		[]*Relationship{edge(RelParent, a, b), edge(RelParent, b, c)},
	)

	// C is A's grandchild, so making C a parent of A closes the loop.
	if !g.WouldCycle(c.ID, a.ID) {
		t.Error("a grandchild was allowed to become their grandparent's parent")
	}
	// A direct reversal is the same problem one generation closer.
	if !g.WouldCycle(b.ID, a.ID) {
		t.Error("a child was allowed to become their parent's parent")
	}
	if !g.WouldCycle(a.ID, a.ID) {
		t.Error("a subject was allowed to become their own parent")
	}

	// An unrelated new parent is fine.
	d := person("s_d", "D")
	if g.WouldCycle(d.ID, a.ID) {
		t.Error("an unrelated subject was refused as a parent")
	}
	// And so is a second parent for an existing child - two parents is the
	// common case, not a limit.
	if g.WouldCycle(d.ID, b.ID) {
		t.Error("a second parent was refused")
	}
}

// TestComplexParentPatterns covers what the model exists to handle: several
// partners, half-siblings, and one animal siring litters with different
// dams - none of which a family-unit record could represent.
func TestComplexParentPatterns(t *testing.T) {
	t.Run("half siblings across two partners", func(t *testing.T) {
		parent := person("s_p", "Parent")
		first := person("s_1", "First partner")
		second := person("s_2", "Second partner")
		childA := person("s_a", "Child A")
		childB := person("s_b", "Child B")

		g := NewGraph(
			[]*Subject{parent, first, second, childA, childB},
			[]*Relationship{
				edge(RelParent, parent, childA), edge(RelParent, first, childA),
				edge(RelParent, parent, childB), edge(RelParent, second, childB),
			},
		)

		tree, err := g.BuildTree(parent.ID, TreeDescendants, 1)
		if err != nil {
			t.Fatalf("BuildTree: %v", err)
		}
		if !contains(tree, childA.ID) || !contains(tree, childB.ID) {
			t.Error("both half-siblings should appear under their shared parent")
		}
		// The other partners are not descendants and must not be pulled in
		// as though they were.
		if contains(tree, first.ID) || contains(tree, second.ID) {
			t.Error("a co-parent appeared in a descendants-only walk")
		}
	})

	t.Run("one sire across several dams", func(t *testing.T) {
		sire := pet("s_sire", "Sire")
		damA, damB := pet("s_dam_a", "Dam A"), pet("s_dam_b", "Dam B")
		pupA, pupB := pet("s_pup_a", "Pup A"), pet("s_pup_b", "Pup B")

		g := NewGraph(
			[]*Subject{sire, damA, damB, pupA, pupB},
			[]*Relationship{
				edge(RelParent, sire, pupA), edge(RelParent, damA, pupA),
				edge(RelParent, sire, pupB), edge(RelParent, damB, pupB),
			},
		)

		tree, err := g.BuildTree(sire.ID, TreeDescendants, 2)
		if err != nil {
			t.Fatalf("BuildTree: %v", err)
		}
		if !contains(tree, pupA.ID) || !contains(tree, pupB.ID) {
			t.Error("a sire's litters by different dams should all appear")
		}
	})
}

// TestOwnershipDoesNotBecomeDescent is the rule that keeps a pedigree from
// being grafted into a family tree.
func TestOwnershipDoesNotBecomeDescent(t *testing.T) {
	grandma := person("s_grandma", "Grandma")
	rufus := pet("s_rufus", "Rufus")
	puppy := pet("s_puppy", "Puppy")

	g := NewGraph(
		[]*Subject{grandma, rufus, puppy},
		[]*Relationship{
			edge(RelOwner, grandma, rufus),
			edge(RelParent, rufus, puppy),
		},
	)

	tree, err := g.BuildTree(grandma.ID, TreeDescendants, 3)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	// The dog hangs off its owner...
	if !contains(tree, rufus.ID) {
		t.Error("an owned animal did not appear on its owner")
	}
	// ...but the walk does not continue through it. Otherwise asking for
	// someone's descendants returns their dog's puppies, and "depth" means
	// two different things in one response.
	if contains(tree, puppy.ID) {
		t.Error("the walk continued through an ownership edge into a pedigree")
	}

	// Rooting the tree at the dog is how you see the dog's own line.
	petTree, err := g.BuildTree(rufus.ID, TreeDescendants, 2)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}
	if !contains(petTree, puppy.ID) {
		t.Error("a tree rooted at the animal did not include its own offspring")
	}
	// And its owner is attached from that side too.
	if !contains(petTree, grandma.ID) {
		t.Error("an animal's tree did not show who it belonged to")
	}
}

// TestOwnershipDoesNotConsumeDepth: a pet must not silently eat a
// generation's budget.
func TestOwnershipDoesNotConsumeDepth(t *testing.T) {
	grandma := person("s_grandma", "Grandma")
	mother := person("s_mother", "Mother")
	rufus := pet("s_rufus", "Rufus")

	g := NewGraph(
		[]*Subject{grandma, mother, rufus},
		[]*Relationship{
			edge(RelParent, grandma, mother),
			edge(RelOwner, mother, rufus),
		},
	)

	// One generation of people, and the animals of everyone reached.
	tree, err := g.BuildTree(grandma.ID, TreeDescendants, 1)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}
	if !contains(tree, mother.ID) {
		t.Fatal("the one requested generation is missing")
	}
	if !contains(tree, rufus.ID) {
		t.Error("an animal was dropped because the depth budget was spent on people")
	}
}

// TestPedigreeCollapse covers why the response is a flat graph rather than
// nested JSON: the same subject is reachable by more than one path, which is
// ordinary in line-breeding and not rare in human genealogy either.
func TestPedigreeCollapse(t *testing.T) {
	ancestor := pet("s_ancestor", "Champion")
	sire := pet("s_sire", "Sire")
	dam := pet("s_dam", "Dam")
	pup := pet("s_pup", "Pup")

	// The champion is the parent of both of the pup's parents.
	g := NewGraph(
		[]*Subject{ancestor, sire, dam, pup},
		[]*Relationship{
			edge(RelParent, ancestor, sire),
			edge(RelParent, ancestor, dam),
			edge(RelParent, sire, pup),
			edge(RelParent, dam, pup),
		},
	)

	tree, err := g.BuildTree(pup.ID, TreeAncestors, 3)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	appearances := 0
	for _, node := range tree.Nodes {
		if node.ID == ancestor.ID {
			appearances++
		}
	}
	if appearances != 1 {
		t.Errorf("the shared ancestor appears %d times in nodes, want exactly 1", appearances)
	}

	// Both paths to it survive as edges, so the client can still draw it on
	// both sides.
	edgesToAncestor := 0
	for _, e := range tree.Edges {
		if e.FromID == ancestor.ID {
			edgesToAncestor++
		}
	}
	if edgesToAncestor != 2 {
		t.Errorf("edges from the shared ancestor = %d, want 2", edgesToAncestor)
	}
}

func TestTreeDirections(t *testing.T) {
	grandma := person("s_grandma", "Grandma")
	mother := person("s_mother", "Mother")
	child := person("s_child", "Child")

	g := NewGraph(
		[]*Subject{grandma, mother, child},
		[]*Relationship{edge(RelParent, grandma, mother), edge(RelParent, mother, child)},
	)

	ancestors, _ := g.BuildTree(mother.ID, TreeAncestors, 2)
	if !contains(ancestors, grandma.ID) || contains(ancestors, child.ID) {
		t.Error("an ancestors walk should reach upward only")
	}

	descendants, _ := g.BuildTree(mother.ID, TreeDescendants, 2)
	if !contains(descendants, child.ID) || contains(descendants, grandma.ID) {
		t.Error("a descendants walk should reach downward only")
	}

	both, _ := g.BuildTree(mother.ID, TreeBoth, 1)
	if !contains(both, grandma.ID) || !contains(both, child.ID) {
		t.Error("a both walk should reach in each direction")
	}
}

func TestTreeRejectsAnUnknownRoot(t *testing.T) {
	g := NewGraph(nil, nil)
	if _, err := g.BuildTree("s_nobody", TreeBoth, 2); err == nil {
		t.Error("a tree was built for a subject that is not in the archive")
	}
}

func contains(tree *Tree, id SubjectID) bool {
	for _, node := range tree.Nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}
