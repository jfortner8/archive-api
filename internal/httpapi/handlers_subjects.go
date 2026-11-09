package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
)

func (s *Server) listSubjectKinds(w http.ResponseWriter, _ *http.Request) error {
	kinds := s.Kinds.All()
	out := make([]gen.SubjectKind, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, toGenSubjectKind(k))
	}
	return writeJSON(w, http.StatusOK, struct {
		Data []gen.SubjectKind `json:"data"`
	}{Data: out})
}

func (s *Server) listSubjects(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	kind := r.URL.Query().Get("kind")
	for name := range r.URL.Query() {
		if name != "kind" {
			return errBadRequest(fmt.Sprintf("unknown query parameter %q", name))
		}
	}
	if kind != "" {
		if _, ok := s.Kinds.Get(kind); !ok {
			return errBadRequest(fmt.Sprintf("%q is not a known subject kind", kind))
		}
	}

	subjects, err := s.Store.ListSubjects(r.Context(), member.ArchiveID, kind)
	if err != nil {
		return err
	}

	urls := s.resolveSubjectURLs(r.Context(), subjects)
	out := make([]gen.Subject, 0, len(subjects))
	for i := range subjects {
		out = append(out, toGenSubject(&subjects[i], urls))
	}

	return writeJSON(w, http.StatusOK, struct {
		Data []gen.Subject `json:"data"`
	}{Data: out})
}

func (s *Server) getSubject(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	subject, err := s.Store.GetSubject(r.Context(), member.ArchiveID, domain.SubjectID(r.PathValue("subjectId")))
	if err != nil {
		return err
	}
	return s.writeSubject(w, r, http.StatusOK, &subject)
}

func (s *Server) createSubject(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	var body gen.SubjectCreate
	if err := readJSON(r, &body); err != nil {
		return err
	}

	subject := &domain.Subject{
		ID:          domain.NewSubjectID(),
		ArchiveID:   member.ArchiveID,
		Kind:        body.Kind,
		DisplayName: body.DisplayName,
		Birth:       fromGenDate(body.Birth),
		Death:       fromGenDate(body.Death),
	}
	if body.GivenNames != nil {
		subject.GivenNames = *body.GivenNames
	}
	if body.FamilyName != nil {
		subject.FamilyName = *body.FamilyName
	}
	if body.Aliases != nil {
		subject.Aliases = *body.Aliases
	}
	if body.Notes != nil {
		subject.Notes = *body.Notes
	}
	if body.Attributes != nil {
		subject.Attributes = *body.Attributes
	}
	if body.CoverItemId != nil {
		subject.CoverItemID = domain.ItemID(*body.CoverItemId)
	}

	if err := s.Store.CreateSubject(r.Context(), subject); err != nil {
		return err
	}
	return s.writeSubject(w, r, http.StatusCreated, subject)
}

var subjectPatchFields = map[string]bool{
	"displayName": true, "givenNames": true, "familyName": true, "aliases": true,
	"birth": true, "death": true, "notes": true, "attributes": true, "coverItemId": true,
}

func (s *Server) patchSubject(w http.ResponseWriter, r *http.Request) error {
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
		if !subjectPatchFields[name] {
			return errBadRequest(fmt.Sprintf("unknown field %q", name))
		}
	}

	patch, err := buildSubjectPatch(raw)
	if err != nil {
		return err
	}

	subject, err := s.Store.PatchSubject(r.Context(), member.ArchiveID, domain.SubjectID(r.PathValue("subjectId")), patch, ifVersion)
	if err != nil {
		return err
	}

	return s.writeSubject(w, r, http.StatusOK, &subject)
}

func buildSubjectPatch(raw map[string]json.RawMessage) (domain.SubjectPatch, error) {
	var patch domain.SubjectPatch

	decode := func(field string, into any) (bool, error) {
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

	if value, ok := raw["displayName"]; ok {
		var name string
		if err := json.Unmarshal(value, &name); err != nil {
			return patch, errBadRequest("displayName is not valid")
		}
		patch.DisplayName = domain.SetTo(name)
	}

	var given, family, notes string
	if present, err := decode("givenNames", &given); err != nil {
		return patch, err
	} else if present {
		patch.GivenNames = domain.SetTo(given)
	}
	if present, err := decode("familyName", &family); err != nil {
		return patch, err
	} else if present {
		patch.FamilyName = domain.SetTo(family)
	}
	if present, err := decode("notes", &notes); err != nil {
		return patch, err
	} else if present {
		patch.Notes = domain.SetTo(notes)
	}

	var aliases []string
	if present, err := decode("aliases", &aliases); err != nil {
		return patch, err
	} else if present {
		if aliases == nil {
			aliases = []string{}
		}
		patch.Aliases = domain.SetTo(aliases)
	}

	for field, target := range map[string]*domain.Opt[domain.ArchiveDate]{
		"birth": &patch.Birth,
		"death": &patch.Death,
	} {
		var date gen.ArchiveDate
		present, err := decode(field, &date)
		if err != nil {
			return patch, err
		}
		if !present {
			continue
		}
		if d := fromGenDate(&date); d != nil && date.Kind != "" {
			*target = domain.SetTo(*d)
		} else {
			*target = domain.Clear[domain.ArchiveDate]()
		}
	}

	var attributes map[string]any
	if present, err := decode("attributes", &attributes); err != nil {
		return patch, err
	} else if present {
		patch.Attributes = domain.SetTo(attributes)
	}

	var coverItemID string
	if present, err := decode("coverItemId", &coverItemID); err != nil {
		return patch, err
	} else if present {
		if coverItemID == "" {
			patch.CoverItemID = domain.Clear[domain.ItemID]()
		} else {
			patch.CoverItemID = domain.SetTo(domain.ItemID(coverItemID))
		}
	}

	return patch, nil
}

func (s *Server) deleteSubject(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	ifVersion, err := parseIfMatch(r)
	if err != nil {
		return err
	}

	if err := s.Store.DeleteSubject(r.Context(), member.ArchiveID, domain.SubjectID(r.PathValue("subjectId")), ifVersion); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// getSubjectTree walks the archive's graph from one subject.
func (s *Server) getSubjectTree(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())
	query := r.URL.Query()

	for name := range query {
		switch name {
		case "depth", "direction":
		default:
			return errBadRequest(fmt.Sprintf("unknown query parameter %q", name))
		}
	}

	depth := 3
	if raw := query.Get("depth"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 10 {
			return errBadRequest("depth must be between 0 and 10")
		}
		depth = parsed
	}

	direction := domain.TreeBoth
	switch query.Get("direction") {
	case "", "both":
	case "ancestors":
		direction = domain.TreeAncestors
	case "descendants":
		direction = domain.TreeDescendants
	default:
		return errBadRequest(`direction must be "ancestors", "descendants" or "both"`)
	}

	graph, err := s.Store.LoadGraph(r.Context(), member.ArchiveID)
	if err != nil {
		return err
	}

	root := domain.SubjectID(r.PathValue("subjectId"))
	tree, err := graph.BuildTree(root, direction, depth)
	if err != nil {
		// The only way BuildTree fails is an unknown root, which from the
		// caller's side is indistinguishable from a subject in someone
		// else's archive - and must stay that way.
		return errNotFound()
	}

	subjects := make([]domain.Subject, 0, len(tree.Nodes))
	for _, node := range tree.Nodes {
		subjects = append(subjects, *node)
	}
	urls := s.resolveSubjectURLs(r.Context(), subjects)

	out := gen.SubjectTree{RootId: string(tree.RootID)}
	for i := range subjects {
		out.Nodes = append(out.Nodes, toGenSubject(&subjects[i], urls))
	}
	for _, e := range tree.Edges {
		out.Edges = append(out.Edges, toGenRelationship(e))
	}
	if out.Nodes == nil {
		out.Nodes = []gen.Subject{}
	}
	if out.Edges == nil {
		out.Edges = []gen.Relationship{}
	}

	return writeJSON(w, http.StatusOK, out)
}

func (s *Server) listRelationships(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	relationships, err := s.Store.ListRelationships(r.Context(), member.ArchiveID)
	if err != nil {
		return err
	}

	out := make([]gen.Relationship, 0, len(relationships))
	for i := range relationships {
		out = append(out, toGenRelationship(&relationships[i]))
	}
	return writeJSON(w, http.StatusOK, struct {
		Data []gen.Relationship `json:"data"`
	}{Data: out})
}

func (s *Server) putRelationship(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	var body gen.RelationshipCreate
	if err := readJSON(r, &body); err != nil {
		return err
	}

	rel := &domain.Relationship{
		ArchiveID: member.ArchiveID,
		Type:      domain.RelType(body.Type),
		FromID:    domain.SubjectID(body.FromSubjectId),
		ToID:      domain.SubjectID(body.ToSubjectId),
		Start:     fromGenDate(body.Start),
		End:       fromGenDate(body.End),
	}
	if body.Notes != nil {
		rel.Notes = *body.Notes
	}

	if err := s.Store.PutRelationship(r.Context(), rel); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, toGenRelationship(rel))
}

func (s *Server) deleteRelationship(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	if err := s.Store.DeleteRelationship(r.Context(), member.ArchiveID, domain.RelID(r.PathValue("relationshipId"))); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) writeSubject(w http.ResponseWriter, r *http.Request, status int, subject *domain.Subject) error {
	urls := s.resolveSubjectURLs(r.Context(), []domain.Subject{*subject})
	w.Header().Set("ETag", etagFor(subject.Version))
	return writeJSON(w, status, toGenSubject(subject, urls))
}

// resolveSubjectURLs presigns each subject's portrait, in bulk.
func (s *Server) resolveSubjectURLs(ctx context.Context, subjects []domain.Subject) map[domain.SubjectID]string {
	urls := make(map[domain.SubjectID]string, len(subjects))
	for i := range subjects {
		if subjects[i].CoverKey == "" {
			continue
		}
		if url, err := s.Files.PresignDownload(ctx, subjects[i].CoverKey); err == nil {
			urls[subjects[i].ID] = url
		}
	}
	return urls
}
