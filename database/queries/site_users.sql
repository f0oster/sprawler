-- name: UpsertSiteUser :exec
INSERT INTO site_users (
    id, site_id, email, expiration, is_email_authentication_guest_user,
    is_hidden_in_ui, is_share_by_email_guest_user, is_site_admin,
    login_name, principal_type, title, userid_name_id, userid_name_id_issuer,
    user_principal_name, updated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id, site_id) DO UPDATE SET
    email = excluded.email,
    expiration = excluded.expiration,
    is_email_authentication_guest_user = excluded.is_email_authentication_guest_user,
    is_hidden_in_ui = excluded.is_hidden_in_ui,
    is_share_by_email_guest_user = excluded.is_share_by_email_guest_user,
    is_site_admin = excluded.is_site_admin,
    login_name = excluded.login_name,
    principal_type = excluded.principal_type,
    title = excluded.title,
    userid_name_id = excluded.userid_name_id,
    userid_name_id_issuer = excluded.userid_name_id_issuer,
    user_principal_name = excluded.user_principal_name,
    updated = excluded.updated,
    updated_at = CURRENT_TIMESTAMP;

-- name: InsertSiteUser :exec
INSERT INTO site_users (
    id, site_id, email, expiration, is_email_authentication_guest_user,
    is_hidden_in_ui, is_share_by_email_guest_user, is_site_admin,
    login_name, principal_type, title, userid_name_id, userid_name_id_issuer,
    user_principal_name, updated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

