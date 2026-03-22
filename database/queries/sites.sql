-- name: UpsertSite :exec
INSERT INTO sites (
    site_id, site_url, time_created, modified, title, template_name,
    created_by_email, group_id, last_activity_on, storage_used, lock_state
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(site_id) DO UPDATE SET
    site_url = excluded.site_url,
    time_created = excluded.time_created,
    modified = excluded.modified,
    title = excluded.title,
    template_name = excluded.template_name,
    created_by_email = excluded.created_by_email,
    group_id = excluded.group_id,
    last_activity_on = excluded.last_activity_on,
    storage_used = excluded.storage_used,
    lock_state = excluded.lock_state,
    updated_at = CURRENT_TIMESTAMP;

-- name: InsertSite :exec
INSERT INTO sites (
    site_id, site_url, time_created, modified, title, template_name,
    created_by_email, group_id, last_activity_on, storage_used, lock_state
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);