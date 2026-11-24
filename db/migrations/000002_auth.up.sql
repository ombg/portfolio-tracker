-- Login and sessions through fixed-purpose SECURITY DEFINER functions (D-32, D-33).
-- pt_app has no privileges on core.users or core.sessions; these functions are its only way in.
-- Every function pins search_path and qualifies names, and EXECUTE is revoked from PUBLIC.

-- Returns the password hash of an enabled user so portfolio-api can verify it (argon2id runs in Go).
CREATE FUNCTION core.auth_user_for_login(p_email text)
    RETURNS TABLE (user_id uuid, password_hash text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    AS $$
        SELECT u.id, u.password_hash
        FROM core.users u
        WHERE lower(u.email) = lower(p_email) AND u.disabled_at IS NULL
    $$;

-- Creates a session for an enabled user and drops that user's expired sessions.
CREATE FUNCTION core.session_create(p_user_id uuid, p_ttl_seconds integer)
    RETURNS TABLE (session_id uuid, expires_at timestamptz)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    AS $$
    BEGIN
        IF p_ttl_seconds IS NULL OR p_ttl_seconds < 60 OR p_ttl_seconds > 90 * 86400 THEN
            RAISE EXCEPTION 'session ttl out of range: %', p_ttl_seconds USING ERRCODE = '22023';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM core.users u WHERE u.id = p_user_id AND u.disabled_at IS NULL) THEN
            RAISE EXCEPTION 'unknown or disabled user' USING ERRCODE = '28000';
        END IF;
        DELETE FROM core.sessions s WHERE s.user_id = p_user_id AND s.expires_at <= now();
        RETURN QUERY
            INSERT INTO core.sessions AS s (user_id, expires_at)
            VALUES (p_user_id, now() + make_interval(secs => p_ttl_seconds))
            RETURNING s.id, s.expires_at;
    END
    $$;

-- Resolves a session to its user and records activity. Returns no row when the session is
-- unknown, expired, idle for longer than p_idle_seconds, or its user is disabled.
CREATE FUNCTION core.session_user_id(p_session_id uuid, p_idle_seconds integer)
    RETURNS uuid
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    AS $$
        UPDATE core.sessions s
        SET last_seen_at = now()
        FROM core.users u
        WHERE s.id = p_session_id
          AND u.id = s.user_id
          AND u.disabled_at IS NULL
          AND s.expires_at > now()
          AND s.last_seen_at > now() - make_interval(secs => p_idle_seconds)
        RETURNING s.user_id
    $$;

CREATE FUNCTION core.session_revoke(p_session_id uuid)
    RETURNS void
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    AS $$ DELETE FROM core.sessions s WHERE s.id = p_session_id $$;

-- Profile of the signed-in user only: the row is selected by app.user_id, never by a parameter.
CREATE FUNCTION core.current_user_profile()
    RETURNS TABLE (id uuid, email text, display_name text, reporting_currency char(3))
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, pg_temp
    AS $$
        SELECT u.id, u.email, u.display_name, u.reporting_currency
        FROM core.users u
        WHERE u.id = core.current_user_id() AND u.disabled_at IS NULL
    $$;

REVOKE ALL ON FUNCTION
    core.auth_user_for_login(text),
    core.session_create(uuid, integer),
    core.session_user_id(uuid, integer),
    core.session_revoke(uuid),
    core.current_user_profile()
FROM PUBLIC;

GRANT EXECUTE ON FUNCTION
    core.auth_user_for_login(text),
    core.session_create(uuid, integer),
    core.session_user_id(uuid, integer),
    core.session_revoke(uuid),
    core.current_user_profile()
TO pt_app;
