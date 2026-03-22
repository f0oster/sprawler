-- name: UpsertUserProfile :exec
INSERT INTO user_profiles (
    personal_url, doc_id, aad_object_id, account_name,
    personal_site_instantiation_state, profile_sid, user_profile_guid,
    user_principal_name, last_modified_time
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(personal_url) DO UPDATE SET
    doc_id = excluded.doc_id,
    aad_object_id = excluded.aad_object_id,
    account_name = excluded.account_name,
    personal_site_instantiation_state = excluded.personal_site_instantiation_state,
    profile_sid = excluded.profile_sid,
    user_profile_guid = excluded.user_profile_guid,
    user_principal_name = excluded.user_principal_name,
    last_modified_time = excluded.last_modified_time,
    updated_at = CURRENT_TIMESTAMP;

-- name: InsertUserProfile :exec
INSERT INTO user_profiles (
    personal_url, doc_id, aad_object_id, account_name,
    personal_site_instantiation_state, profile_sid, user_profile_guid,
    user_principal_name, last_modified_time
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);