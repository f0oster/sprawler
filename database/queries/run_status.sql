-- name: CreateRunStatus :exec
INSERT INTO run_status (id, full_export_completed)
VALUES (?, ?);

-- name: UpdateRunStatus :exec
UPDATE run_status 
SET full_export_completed = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: GetRunStatus :one
SELECT id, full_export_completed
FROM run_status 
WHERE id = ?;