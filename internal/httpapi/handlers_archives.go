package httpapi

import (
	"errors"
	"net/http"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
	"github.com/jfortner8/archive-api/internal/store"
)

// getMe is the bootstrap call, and the only place an account is provisioned.
//
// This service is never told that someone signed up - Cognito is spoken to
// entirely by the frontend, and nothing here holds a credential that could
// create a user. So the first time a verified token arrives from an account
// with no archive, one is made for them. Provisioning on first sight rather
// than through a signup hook is what keeps that separation intact.
func (s *Server) getMe(w http.ResponseWriter, r *http.Request) error {
	account, ok := accountIDFrom(r.Context())
	if !ok {
		return errInternal()
	}

	displayName := r.URL.Query().Get("displayName")

	memberships, err := s.Store.ListArchivesForAccount(r.Context(), account)
	if err != nil {
		return err
	}

	if len(memberships) == 0 {
		name := displayName
		if name == "" {
			name = "My archive"
		} else {
			name += "'s archive"
		}

		archive, err := s.Store.CreateArchive(r.Context(), name, account, displayName)
		if err != nil {
			return err
		}
		memberships = []domain.Member{{
			ArchiveID:   archive.ID,
			AccountID:   account,
			Role:        domain.RoleOwner,
			DisplayName: displayName,
		}}
	}

	out := gen.Me{
		AccountId:        string(account),
		DisplayName:      optional(displayName),
		DefaultArchiveId: string(memberships[0].ArchiveID),
		Archives:         s.membershipsToGen(r, memberships),
	}
	return writeJSON(w, http.StatusOK, out)
}

// membershipsToGen fills in each archive's name.
//
// The membership row does not carry it, so this is one read per archive.
// That is fine because a person belongs to a handful of archives, not
// thousands - and it is worth noticing that this is the only fan-out left in
// a read path.
func (s *Server) membershipsToGen(r *http.Request, memberships []domain.Member) []gen.ArchiveMembership {
	out := make([]gen.ArchiveMembership, 0, len(memberships))
	for _, m := range memberships {
		entry := gen.ArchiveMembership{
			ArchiveId: string(m.ArchiveID),
			Role:      gen.Role(m.Role),
		}
		if archive, err := s.Store.GetArchive(r.Context(), m.ArchiveID); err == nil {
			entry.Name = optional(archive.Name)
		}
		out = append(out, entry)
	}
	return out
}

func (s *Server) listArchives(w http.ResponseWriter, r *http.Request) error {
	account, ok := accountIDFrom(r.Context())
	if !ok {
		return errInternal()
	}

	memberships, err := s.Store.ListArchivesForAccount(r.Context(), account)
	if err != nil {
		return err
	}

	return writeJSON(w, http.StatusOK, struct {
		Data []gen.ArchiveMembership `json:"data"`
	}{Data: s.membershipsToGen(r, memberships)})
}

func (s *Server) createArchive(w http.ResponseWriter, r *http.Request) error {
	account, ok := accountIDFrom(r.Context())
	if !ok {
		return errInternal()
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	if body.Name == "" {
		return errBadRequest("name is required")
	}

	archive, err := s.Store.CreateArchive(r.Context(), body.Name, account, "")
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, toGenArchive(archive))
}

func (s *Server) getArchive(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	archive, err := s.Store.GetArchive(r.Context(), member.ArchiveID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, toGenArchive(archive))
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	members, err := s.Store.ListMembers(r.Context(), member.ArchiveID)
	if err != nil {
		return err
	}

	out := make([]gen.Member, 0, len(members))
	for _, m := range members {
		out = append(out, toGenMember(m))
	}
	return writeJSON(w, http.StatusOK, struct {
		Data []gen.Member `json:"data"`
	}{Data: out})
}

func (s *Server) putMember(w http.ResponseWriter, r *http.Request) error {
	caller, _ := memberFrom(r.Context())
	if !caller.Role.Can(domain.ActionManageMembers) {
		return errForbidden("only an owner can manage members")
	}

	var body struct {
		Role        gen.Role `json:"role"`
		DisplayName *string  `json:"displayName,omitempty"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}

	role := domain.Role(body.Role)
	if !role.Valid() {
		return errBadRequest("role must be owner, editor or viewer")
	}

	member := domain.Member{
		ArchiveID: caller.ArchiveID,
		AccountID: domain.AccountID(r.PathValue("accountId")),
		Role:      role,
	}
	if body.DisplayName != nil {
		member.DisplayName = *body.DisplayName
	}

	if err := s.Store.PutMember(r.Context(), member); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, toGenMember(member))
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) error {
	caller, _ := memberFrom(r.Context())
	if !caller.Role.Can(domain.ActionManageMembers) {
		return errForbidden("only an owner can manage members")
	}

	target := domain.AccountID(r.PathValue("accountId"))

	existing, err := s.Store.GetMember(r.Context(), caller.ArchiveID, target)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errNotFound()
		}
		return err
	}

	// An archive whose last owner leaves can never be administered again -
	// no one could add members, change roles, or delete it. Refusing is
	// kinder than letting someone strand their own archive.
	if existing.Role == domain.RoleOwner {
		members, err := s.Store.ListMembers(r.Context(), caller.ArchiveID)
		if err != nil {
			return err
		}
		owners := 0
		for _, m := range members {
			if m.Role == domain.RoleOwner {
				owners++
			}
		}
		if owners <= 1 {
			return errConflict("an archive must keep at least one owner")
		}
	}

	if err := s.Store.RemoveMember(r.Context(), caller.ArchiveID, target); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// listItemTypes serves the catalogue the UI builds its forms and viewers
// from. It is compiled into the binary, so the ETag is the registry's
// identity and a client that already has it gets a 304.
func (s *Server) listItemTypes(w http.ResponseWriter, r *http.Request) error {
	etag := s.typesETag()
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	types := s.Types.All()
	out := make([]gen.ItemType, 0, len(types))
	for _, t := range types {
		out = append(out, toGenItemType(t))
	}

	w.Header().Set("ETag", etag)
	return writeJSON(w, http.StatusOK, struct {
		Data []gen.ItemType `json:"data"`
	}{Data: out})
}
