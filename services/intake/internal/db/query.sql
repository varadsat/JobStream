-- name: InsertApplication :one
INSERT INTO applications (
    user_id, job_title, company, url, source, status, applied_at, schema_version
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING id, user_id, job_title, company, url, source, status, applied_at, created_at, schema_version;

-- name: GetApplicationByIDAndUserID :one
SELECT id, user_id, job_title, company, url, source, status, applied_at, created_at, schema_version
FROM applications
WHERE id = $1 AND user_id = $2
LIMIT 1;
