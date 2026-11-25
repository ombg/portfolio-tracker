-- ptctl: runs as the database owner (D-32).

-- name: CreateUser :one
INSERT INTO core.users (email, password_hash, display_name, reporting_currency)
VALUES (@email, @password_hash, @display_name, @reporting_currency)
RETURNING id;

-- name: CreatePortfolio :exec
INSERT INTO core.portfolios (user_id, name) VALUES (@user_id, @name);

-- name: SetPasswordHash :one
UPDATE core.users SET password_hash = @password_hash
WHERE lower(email) = lower(@email::text)
RETURNING id;

-- name: DeleteUserSessions :exec
DELETE FROM core.sessions WHERE user_id = @user_id;
