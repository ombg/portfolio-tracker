-- M2 #17: reusable preview corrections (D-36). Owner: importer.
-- A rule maps something the broker's file says to what it means, per user and broker:
--   kind 'action': the parser's action key (Questrade "Activity Type|Action", Wealthsimple
--                  "activity_type|activity_sub_type") -> {"type": <ledger type>}, used only where the parser has
--                  no mapping of its own;
--   kind 'symbol': the file's symbol or broker code -> {"symbol", "exchange"}.
-- Rules apply whenever a batch that is not yet committed is parsed again; committed rows never change (FR-3 item 8).

CREATE TABLE imports.import_rules (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES core.users (id) ON DELETE RESTRICT,
    broker_id  uuid NOT NULL REFERENCES core.brokers (id),
    kind       text NOT NULL CHECK (kind IN ('action', 'symbol')),
    source_key text NOT NULL CHECK (btrim(source_key) <> ''),
    target     jsonb NOT NULL CHECK (jsonb_typeof(target) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT import_rules_key UNIQUE (user_id, broker_id, kind, source_key)
);

ALTER TABLE imports.import_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE imports.import_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY owner ON imports.import_rules USING (user_id = core.current_user_id())
    WITH CHECK (user_id = core.current_user_id());

-- D-35: the importer writes its own schema; portfolio-api reads it. Nobody deletes.
GRANT SELECT, INSERT, UPDATE ON imports.import_rules TO pt_importer;
GRANT SELECT ON imports.import_rules TO pt_app;
