DROP FUNCTION IF EXISTS core.current_user_profile();
DROP FUNCTION IF EXISTS core.session_revoke(uuid);
DROP FUNCTION IF EXISTS core.session_user_id(uuid, integer);
DROP FUNCTION IF EXISTS core.session_create(uuid, integer);
DROP FUNCTION IF EXISTS core.auth_user_for_login(text);
