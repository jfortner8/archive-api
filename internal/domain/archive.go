package domain

import "time"

// Archive is the unit of sharing and the thing everything else belongs to.
//
// Replacing per-account scoping with a shared archive is what makes a family
// archive collaborative rather than one person's private copy. It is also a
// security improvement: the archive id becomes part of the database key, so
// scoping is enforced by the query rather than by a comparison in Go after
// someone else's row has already been read into memory.
type Archive struct {
	ID        ArchiveID `json:"id" dynamodbav:"-"`
	Name      string    `json:"name" dynamodbav:"name"`
	CreatedBy AccountID `json:"createdBy" dynamodbav:"createdBy"`
	CreatedAt time.Time `json:"createdAt" dynamodbav:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" dynamodbav:"updatedAt"`

	// Version is bumped by any write anywhere in the archive. It is what lets
	// a collection or spine response carry a cheap ETag: hash the filter plus
	// this number, and a client that already has the current version gets a
	// 304 with no body instead of the whole archive again.
	Version int64 `json:"-" dynamodbav:"version"`
}

// Role is what a member may do. Roles are per-archive, which is exactly why
// they live here and not in Cognito - `cognito:groups` is global, and the
// same person can reasonably be an owner of one archive and a viewer of
// another.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// Action is a thing a caller might try to do. Keeping these as named actions
// rather than checking role strings at each call site means the rules live in
// one table-tested function instead of being scattered across handlers.
type Action string

const (
	ActionRead          Action = "read"
	ActionWrite         Action = "write"
	ActionManageMembers Action = "manage_members"
	ActionDeleteArchive Action = "delete_archive"
)

var rolePermissions = map[Role]map[Action]bool{
	RoleOwner: {
		ActionRead: true, ActionWrite: true,
		ActionManageMembers: true, ActionDeleteArchive: true,
	},
	RoleEditor: {
		ActionRead: true, ActionWrite: true,
	},
	RoleViewer: {
		ActionRead: true,
	},
}

// Can reports whether this role permits the action. An unknown role permits
// nothing, so a corrupt or future role fails closed.
func (r Role) Can(a Action) bool {
	return rolePermissions[r][a]
}

// Valid reports whether this is a role we recognise.
func (r Role) Valid() bool {
	_, ok := rolePermissions[r]
	return ok
}

// Member joins an account to an archive.
type Member struct {
	ArchiveID ArchiveID `json:"archiveId" dynamodbav:"-"`
	AccountID AccountID `json:"accountId" dynamodbav:"-"`
	Role      Role      `json:"role" dynamodbav:"role"`

	// DisplayName is supplied by the frontend, because this service never
	// learns one otherwise. It verifies Cognito access tokens, whose claims
	// carry only `sub`, `username`, `client_id` and `exp` - an email lives in
	// the ID token, which the verifier deliberately rejects. Without this the
	// members list would be a column of UUIDs.
	DisplayName string `json:"displayName,omitempty" dynamodbav:"displayName,omitempty"`

	JoinedAt time.Time `json:"joinedAt" dynamodbav:"joinedAt"`
}
