-- Login and sessions: only the D-32 functions, run as pt_app.

-- name: AuthUserForLogin :one
SELECT user_id::uuid AS user_id, password_hash::text AS password_hash
FROM core.auth_user_for_login(@email::text);

-- name: SessionCreate :one
SELECT session_id::uuid AS session_id, expires_at::timestamptz AS expires_at
FROM core.session_create(@user_id::uuid, @ttl_seconds::integer);

-- name: SessionUserID :one
SELECT core.session_user_id(@session_id::uuid, @idle_seconds::integer)::uuid AS user_id;

-- name: SessionRevoke :exec
SELECT core.session_revoke(@session_id::uuid);

-- name: CurrentUserProfile :one
SELECT id::uuid AS id, email::text AS email, display_name::text AS display_name,
       reporting_currency::text AS reporting_currency
FROM core.current_user_profile();
