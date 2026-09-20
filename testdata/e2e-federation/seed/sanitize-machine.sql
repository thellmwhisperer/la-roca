PRAGMA secure_delete = ON;
BEGIN;
UPDATE sessions SET machine = 'synthetic-e2e' WHERE machine IS NOT NULL;
UPDATE exchanges SET machine = 'synthetic-e2e' WHERE machine IS NOT NULL;
UPDATE tool_uses SET machine = 'synthetic-e2e' WHERE machine IS NOT NULL;
UPDATE thinking_blocks SET machine = 'synthetic-e2e' WHERE machine IS NOT NULL;
COMMIT;
VACUUM;
