package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)

// Store is the authz Postgres layer.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a new authz store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func newID() string { return ulid.Make().String() }

// --- Users ---

// UpsertUser creates or updates a user from OIDC claims. Returns the user.
// Empty email/username values are treated as "not provided" and will not
// overwrite existing non-empty values in the database.
func (s *Store) UpsertUser(ctx context.Context, sub, email, username string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, username, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (id) DO UPDATE SET
			email    = CASE WHEN $2 = '' THEN users.email    ELSE $2 END,
			username = CASE WHEN $3 = '' THEN users.username  ELSE $3 END,
			updated_at = now()
		RETURNING id, email, username, is_admin, disabled, created_at, updated_at
	`, sub, email, username).Scan(&u.ID, &u.Email, &u.Username, &u.IsAdmin, &u.Disabled, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert user: %w", err)
	}
	return &u, nil
}

// GetUser returns a user by ID.
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, username, is_admin, disabled, created_at, updated_at
		FROM users WHERE id = $1
	`, id).Scan(&u.ID, &u.Email, &u.Username, &u.IsAdmin, &u.Disabled, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authz.GetUser: %w", err)
	}
	return &u, nil
}

// ListUsers returns all users ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, username, is_admin, disabled, created_at, updated_at
		FROM users ORDER BY username
	`)
	if err != nil {
		return nil, fmt.Errorf("authz.ListUsers: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Username, &u.IsAdmin, &u.Disabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("authz.ListUsers: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetAdmin sets or clears the is_admin flag.
func (s *Store) SetAdmin(ctx context.Context, userID string, isAdmin bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET is_admin = $1, updated_at = now() WHERE id = $2`, isAdmin, userID)
	if err != nil {
		return fmt.Errorf("authz.SetAdmin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDisabled sets or clears the disabled flag. A disabled user cannot log in
// and all authenticated API access is rejected; group memberships and user
// tokens are untouched, so re-enabling restores access.
func (s *Store) SetDisabled(ctx context.Context, userID string, disabled bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET disabled = $1, updated_at = now() WHERE id = $2`, disabled, userID)
	if err != nil {
		return fmt.Errorf("authz.SetDisabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IsUserDisabled reports whether the user exists and is disabled. Returns
// ErrNotFound when the user has no registry row yet (e.g. a first-seen OIDC
// identity that has not been persisted).
func (s *Store) IsUserDisabled(ctx context.Context, id string) (bool, error) {
	var disabled bool
	err := s.pool.QueryRow(ctx, `SELECT disabled FROM users WHERE id = $1`, id).Scan(&disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("authz.IsUserDisabled: %w", err)
	}
	return disabled, nil
}

// ClearUsername resets a user's stored username to '', triggering a fresh
// sync from the OIDC userinfo endpoint on the user's next authenticated request.
func (s *Store) ClearUsername(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET username = '', updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("authz.ClearUsername: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LocalUserIDPrefix namespaces locally-managed accounts so their IDs can
// never collide with OIDs subs from an external IdP.
const LocalUserIDPrefix = "local:"

// CreateLocalUser inserts a locally-managed user with password credentials.
// Unlike UpsertUser it fails with ErrAlreadyExists when the ID is taken, so
// a caller can never silently overwrite an existing account's credentials.
func (s *Store) CreateLocalUser(ctx context.Context, id, email, username, passwordHash string, isAdmin bool) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, username, is_admin, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())
		RETURNING id, email, username, is_admin, disabled, created_at, updated_at
	`, id, email, username, isAdmin, passwordHash).Scan(&u.ID, &u.Email, &u.Username, &u.IsAdmin, &u.Disabled, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("authz.CreateLocalUser: %w", err)
	}
	return &u, nil
}

// UserPasswordHash returns the stored password hash for a user ('' when the
// user has no local credentials). Kept separate from GetUser so the hash can
// never leak into API responses that serialize User.
func (s *Store) UserPasswordHash(ctx context.Context, id string) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, id).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("authz.UserPasswordHash: %w", err)
	}
	return hash, nil
}

// SetPassword stores a new password hash for a user and bumps the user's
// token version in the same statement, so every previously issued local
// session JWT is invalidated the moment the password changes (issue #112).
// Doing it in one UPDATE closes the window where the new hash is stored but
// old tokens would still validate.
func (s *Store) SetPassword(ctx context.Context, id, hash string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE users SET password_hash = $1, token_version = token_version + 1, updated_at = now()
		WHERE id = $2
	`, hash, id)
	if err != nil {
		return fmt.Errorf("authz.SetPassword: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UserTokenVersion returns the user's current token version. Kept separate
// from GetUser, like UserPasswordHash, so it never reaches serialized API
// responses. The localauth middleware compares it against the claim embedded
// in local session JWTs.
func (s *Store) UserTokenVersion(ctx context.Context, id string) (int64, error) {
	var v int64
	err := s.pool.QueryRow(ctx, `SELECT token_version FROM users WHERE id = $1`, id).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("authz.UserTokenVersion: %w", err)
	}
	return v, nil
}

// BumpTokenVersion increments the user's token version, invalidating all
// previously issued local session JWTs without changing the password
// ("sign out everywhere").
func (s *Store) BumpTokenVersion(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET token_version = token_version + 1, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("authz.BumpTokenVersion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser removes a user registry row. Group memberships and user tokens
// cascade (the only remaining FKs to users since the resource_groups
// redesign in migration 0016). OIDC users reappear via identity persistence
// on their next login; deleting a local user revokes it.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("authz.DeleteUser: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// --- Groups ---

// CreateGroup creates a new group.
func (s *Store) CreateGroup(ctx context.Context, name, description string) (*Group, error) {
	g := Group{ID: newID(), Name: name, Description: description}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO groups (id, name, description, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		RETURNING created_at, updated_at
	`, g.ID, g.Name, g.Description).Scan(&g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create group: %w", err)
	}
	return &g, nil
}

// GetGroup returns a group by ID.
func (s *Store) GetGroup(ctx context.Context, id string) (*Group, error) {
	var g Group
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM groups WHERE id = $1
	`, id).Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authz.GetGroup: %w", err)
	}
	return &g, nil
}

// ListGroups returns all groups ordered by name.
func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM groups ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("authz.ListGroups: %w", err)
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, fmt.Errorf("authz.ListGroups: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UpdateGroup updates a group's name and description.
func (s *Store) UpdateGroup(ctx context.Context, id, name, description string) (*Group, error) {
	var g Group
	err := s.pool.QueryRow(ctx, `
		UPDATE groups SET name = $2, description = $3, updated_at = now()
		WHERE id = $1
		RETURNING id, name, description, created_at, updated_at
	`, id, name, description).Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authz.UpdateGroup: %w", err)
	}
	return &g, nil
}

// DeleteGroup removes a group. Cascades to members and resource_groups.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM groups WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("authz.DeleteGroup: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddGroupMember adds a user to a group with the given role.
func (s *Store) AddGroupMember(ctx context.Context, groupID, userID string, role GroupRole) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (group_id, user_id) DO UPDATE SET role = $3
	`, groupID, userID, string(role))
	if err != nil {
		return fmt.Errorf("authz.AddGroupMember: %w", err)
	}
	return nil
}

// RemoveGroupMember removes a user from a group.
func (s *Store) RemoveGroupMember(ctx context.Context, groupID, userID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
	if err != nil {
		return fmt.Errorf("authz.RemoveGroupMember: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListGroupMembers returns all users in a group with their roles.
func (s *Store) ListGroupMembers(ctx context.Context, groupID string) ([]MemberWithUser, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email, u.username, u.is_admin, u.disabled, u.created_at, u.updated_at, gm.role
		FROM users u JOIN group_members gm ON gm.user_id = u.id
		WHERE gm.group_id = $1 ORDER BY u.username
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("authz.ListGroupMembers: %w", err)
	}
	defer rows.Close()
	var out []MemberWithUser
	for rows.Next() {
		var m MemberWithUser
		var role string
		if err := rows.Scan(&m.User.ID, &m.User.Email, &m.User.Username, &m.User.IsAdmin, &m.User.Disabled, &m.User.CreatedAt, &m.User.UpdatedAt, &role); err != nil {
			return nil, fmt.Errorf("authz.ListGroupMembers: %w", err)
		}
		m.Role = GroupRole(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- Group Resolution ---

// UserGroupIDs returns all group IDs a user belongs to (flat, no recursion).
func (s *Store) UserGroupIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT group_id FROM group_members WHERE user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("authz.UserGroupIDs: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("authz.UserGroupIDs: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UserGroupRoles returns the user's role in each group they belong to.
func (s *Store) UserGroupRoles(ctx context.Context, userID string) (map[string]GroupRole, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT group_id, role FROM group_members WHERE user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("authz.UserGroupRoles: %w", err)
	}
	defer rows.Close()
	out := make(map[string]GroupRole)
	for rows.Next() {
		var gid, role string
		if err := rows.Scan(&gid, &role); err != nil {
			return nil, fmt.Errorf("authz.UserGroupRoles: %w", err)
		}
		out[gid] = GroupRole(role)
	}
	return out, rows.Err()
}

// --- Resource Groups ---

// ResourceRef identifies one resource for bulk group operations.
type ResourceRef struct {
	Type string
	Name string
}

// EnsureResourceGroupsDefault assigns every listed resource that has no group
// mapping yet to groupID (INSERT … ON CONFLICT DO NOTHING in one statement),
// and reports how many rows it created. Existing mappings are never touched —
// unlike SetResourceGroup this never reassigns, so it is safe to run on every
// boot as a repair for resources created out-of-band (kubectl apply).
func (s *Store) EnsureResourceGroupsDefault(ctx context.Context, refs []ResourceRef, groupID string) (int, error) {
	if len(refs) == 0 {
		return 0, nil
	}
	types := make([]string, len(refs))
	names := make([]string, len(refs))
	for i, r := range refs {
		types[i] = r.Type
		names[i] = r.Name
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO resource_groups (resource_type, resource_name, group_id, public, created_at)
		SELECT t, n, $3, false, now()
		FROM unnest($1::text[], $2::text[]) AS u(t, n)
		ON CONFLICT (resource_type, resource_name) DO NOTHING
	`, types, names, groupID)
	if err != nil {
		return 0, fmt.Errorf("authz.EnsureResourceGroupsDefault: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// SetResourceGroup assigns a resource to a group.
func (s *Store) SetResourceGroup(ctx context.Context, resourceType, resourceName, groupID string, public bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO resource_groups (resource_type, resource_name, group_id, public, created_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (resource_type, resource_name) DO UPDATE SET group_id = $3, public = $4
	`, resourceType, resourceName, groupID, public)
	if err != nil {
		return fmt.Errorf("authz.SetResourceGroup: %w", err)
	}
	return nil
}

// GetResourceGroup returns the group mapping for a resource.
func (s *Store) GetResourceGroup(ctx context.Context, resourceType, resourceName string) (*ResourceGroup, error) {
	var rg ResourceGroup
	err := s.pool.QueryRow(ctx, `
		SELECT resource_type, resource_name, group_id, public, created_at
		FROM resource_groups WHERE resource_type = $1 AND resource_name = $2
	`, resourceType, resourceName).Scan(&rg.ResourceType, &rg.ResourceName, &rg.GroupID, &rg.Public, &rg.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("authz.GetResourceGroup: %w", err)
	}
	return &rg, nil
}

// DeleteResourceGroup removes the group mapping for a resource.
func (s *Store) DeleteResourceGroup(ctx context.Context, resourceType, resourceName string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM resource_groups WHERE resource_type = $1 AND resource_name = $2
	`, resourceType, resourceName)
	if err != nil {
		return fmt.Errorf("authz.DeleteResourceGroup: %w", err)
	}
	return nil
}

// SetResourcePublic toggles the public flag on a resource.
func (s *Store) SetResourcePublic(ctx context.Context, resourceType, resourceName string, public bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE resource_groups SET public = $3 WHERE resource_type = $1 AND resource_name = $2
	`, resourceType, resourceName, public)
	if err != nil {
		return fmt.Errorf("authz.SetResourcePublic: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListResourcesByGroups returns resource names of a type that belong to
// any of the given groups. If includePublic is true, also returns public
// resources from other groups.
func (s *Store) ListResourcesByGroups(ctx context.Context, resourceType string, groupIDs []string, includePublic bool) ([]string, error) {
	var rows pgx.Rows
	var err error
	if includePublic {
		rows, err = s.pool.Query(ctx, `
			SELECT resource_name FROM resource_groups
			WHERE resource_type = $1 AND (group_id = ANY($2) OR public = true)
			ORDER BY resource_name
		`, resourceType, groupIDs)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT resource_name FROM resource_groups
			WHERE resource_type = $1 AND group_id = ANY($2)
			ORDER BY resource_name
		`, resourceType, groupIDs)
	}
	if err != nil {
		return nil, fmt.Errorf("authz.ListResourcesByGroups: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("authz.ListResourcesByGroups: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
