package httpapi

import (
	"context"
	"net/http"

	"github.com/jfortner8/archive-api/internal/authtoken"
	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
	"github.com/jfortner8/archive-api/internal/domain/subjecttypes"
	"github.com/jfortner8/archive-api/internal/store"
)

// The ports handlers depend on, declared here rather than taken as concrete
// types so tests can substitute fakes without an AWS client in sight.

type itemStore interface {
	CreateItem(ctx context.Context, item *domain.Item) error
	GetItem(ctx context.Context, archive domain.ArchiveID, id domain.ItemID) (domain.Item, error)
	ListItems(ctx context.Context, archive domain.ArchiveID, opts store.ListOptions) (store.ItemPage, error)
	PatchItem(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, patch domain.ItemPatch, ifVersion *int64) (domain.Item, error)
	DeleteItem(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, ifVersion *int64) error
	AppendFile(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, file domain.File) (domain.Item, error)
	DeleteFile(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, fileID domain.FileID, ifVersion *int64) (domain.Item, error)
	ReorderFiles(ctx context.Context, archive domain.ArchiveID, id domain.ItemID, order []domain.FileID, ifVersion *int64) (domain.Item, error)
}

type subjectStore interface {
	CreateSubject(ctx context.Context, subject *domain.Subject) error
	GetSubject(ctx context.Context, archive domain.ArchiveID, id domain.SubjectID) (domain.Subject, error)
	ListSubjects(ctx context.Context, archive domain.ArchiveID, kind string) ([]domain.Subject, error)
	PatchSubject(ctx context.Context, archive domain.ArchiveID, id domain.SubjectID, patch domain.SubjectPatch, ifVersion *int64) (domain.Subject, error)
	DeleteSubject(ctx context.Context, archive domain.ArchiveID, id domain.SubjectID, ifVersion *int64) error

	PutRelationship(ctx context.Context, rel *domain.Relationship) error
	DeleteRelationship(ctx context.Context, archive domain.ArchiveID, id domain.RelID) error
	ListRelationships(ctx context.Context, archive domain.ArchiveID) ([]domain.Relationship, error)

	// LoadGraph is two queries and an in-memory build. A graph feature
	// usually implies a graph database; here every entity in an archive
	// shares one partition, so it does not.
	LoadGraph(ctx context.Context, archive domain.ArchiveID) (*domain.Graph, error)
}

type archiveStore interface {
	CreateArchive(ctx context.Context, name string, owner domain.AccountID, ownerName string) (domain.Archive, error)
	GetArchive(ctx context.Context, id domain.ArchiveID) (domain.Archive, error)
	GetMember(ctx context.Context, archive domain.ArchiveID, account domain.AccountID) (domain.Member, error)
	ListMembers(ctx context.Context, archive domain.ArchiveID) ([]domain.Member, error)
	ListArchivesForAccount(ctx context.Context, account domain.AccountID) ([]domain.Member, error)
	PutMember(ctx context.Context, member domain.Member) error
	RemoveMember(ctx context.Context, archive domain.ArchiveID, account domain.AccountID) error
}

// presigner issues the URLs clients use to move bytes to and from S3
// directly, so file contents never pass through this service.
type presigner interface {
	PresignUpload(ctx context.Context, key, contentType string) (string, error)
	PresignDownload(ctx context.Context, key string) (string, error)
}

type tokenVerifier interface {
	Verify(ctx context.Context, token string) (authtoken.Claims, error)
}

// dataStore is the union the Server actually holds. Splitting it into the
// two interfaces above keeps each handler's dependencies readable.
type dataStore interface {
	itemStore
	archiveStore
	subjectStore
}

// Server holds everything the handlers need.
type Server struct {
	Store    dataStore
	Files    presigner
	Verifier tokenVerifier
	Types    *itemtypes.Registry
	Kinds    *subjecttypes.Registry

	// APIKey must match the X-API-Key header on everything except /healthz.
	APIKey string
}

// route is one entry in the table below. Keeping the routes as data rather
// than as a sequence of registration calls lets tests walk them - which is
// what turns "every route is authenticated" from a convention into something
// checked on every build.
type route struct {
	Method  string
	Pattern string
	Handler handlerFunc

	// RequiresArchiveMembership is true for the archive-scoped routes, whose
	// paths carry an {archiveId}.
	RequiresArchiveMembership bool
}

// v1Routes is every authenticated route.
func (s *Server) v1Routes() []route {
	return []route{
		{"GET", "/v1/me", s.getMe, false},
		{"GET", "/v1/item-types", s.listItemTypes, false},
		{"GET", "/v1/subject-kinds", s.listSubjectKinds, false},

		{"GET", "/v1/archives", s.listArchives, false},
		{"POST", "/v1/archives", s.createArchive, false},
		{"GET", "/v1/archives/{archiveId}", s.getArchive, true},
		{"GET", "/v1/archives/{archiveId}/members", s.listMembers, true},
		{"PUT", "/v1/archives/{archiveId}/members/{accountId}", s.putMember, true},
		{"DELETE", "/v1/archives/{archiveId}/members/{accountId}", s.removeMember, true},

		{"GET", "/v1/archives/{archiveId}/items", s.listItems, true},
		{"POST", "/v1/archives/{archiveId}/items", s.createItem, true},
		{"GET", "/v1/archives/{archiveId}/items/{itemId}", s.getItem, true},
		{"PATCH", "/v1/archives/{archiveId}/items/{itemId}", s.patchItem, true},
		{"DELETE", "/v1/archives/{archiveId}/items/{itemId}", s.deleteItem, true},

		{"POST", "/v1/archives/{archiveId}/items/{itemId}/upload-url", s.createUploadURL, true},
		{"POST", "/v1/archives/{archiveId}/items/{itemId}/files", s.attachFile, true},
		{"PUT", "/v1/archives/{archiveId}/items/{itemId}/files/order", s.reorderFiles, true},
		{"DELETE", "/v1/archives/{archiveId}/items/{itemId}/files/{fileId}", s.deleteFile, true},

		{"GET", "/v1/archives/{archiveId}/subjects", s.listSubjects, true},
		{"POST", "/v1/archives/{archiveId}/subjects", s.createSubject, true},
		{"GET", "/v1/archives/{archiveId}/subjects/{subjectId}", s.getSubject, true},
		{"PATCH", "/v1/archives/{archiveId}/subjects/{subjectId}", s.patchSubject, true},
		{"DELETE", "/v1/archives/{archiveId}/subjects/{subjectId}", s.deleteSubject, true},
		{"GET", "/v1/archives/{archiveId}/subjects/{subjectId}/tree", s.getSubjectTree, true},

		{"GET", "/v1/archives/{archiveId}/relationships", s.listRelationships, true},
		{"POST", "/v1/archives/{archiveId}/relationships", s.putRelationship, true},
		{"DELETE", "/v1/archives/{archiveId}/relationships/{relationshipId}", s.deleteRelationship, true},
	}
}

// Routes builds the handler for the whole API.
//
// Authentication is a property of the /v1/ mount, not of each registration.
// The previous version wrapped every route individually, so a new route was
// unprotected unless someone remembered - with a 500 buried in a helper as
// the only guard. Here a route registered on v1 cannot be reached without
// passing the whole chain, and TestEveryRouteRequiresAuth walks the table
// above to prove it stays that way.
func (s *Server) Routes() http.Handler {
	v1 := http.NewServeMux()
	for _, rt := range s.v1Routes() {
		v1.HandleFunc(rt.Method+" "+rt.Pattern, s.handle(rt.Handler))
	}

	protected := chain(v1,
		s.withAPIKey,
		s.withAuth,
		s.withArchiveAccess,
	)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", s.handle(s.healthCheck))
	root.Handle("/v1/", protected)

	// Deliberately no catch-all "/" route. One would match every unrouted
	// request, including a known path reached with the wrong method - so
	// ServeMux would never get to answer 405 and a DELETE to /healthz would
	// read as "no such path". withJSONErrors converts both of ServeMux's own
	// plaintext answers into the envelope instead, which keeps the
	// distinction.

	return chain(root, withRecovery, withRequestID, withLogging, withJSONErrors)
}

func (s *Server) healthCheck(w http.ResponseWriter, _ *http.Request) error {
	return writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
