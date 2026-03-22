-- name: UpsertSiteGroupsBatch :exec
INSERT OR REPLACE INTO site_groups (
    id, title, login_name, description, owner_title, site_id, updated
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: InsertSiteGroupsBatch :exec
INSERT INTO site_groups (
    id, title, login_name, description, owner_title, site_id, updated
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: UpsertGroupMembersBatch :exec
INSERT OR REPLACE INTO group_members (
    id, title, login_name, email, principal_type, user_principal_name, group_id, site_id, updated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertGroupMembersBatch :exec
INSERT INTO group_members (
    id, title, login_name, email, principal_type, user_principal_name, group_id, site_id, updated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);