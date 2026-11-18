-- kadr:bump-schema-version
-- Audit: allow actor_type = 'agent'. Same in-place CHECK widening as 0005;
-- no stored data changes.
PRAGMA writable_schema = ON;
UPDATE sqlite_master
   SET sql = replace(sql, 'CHECK (actor_type IN (''admin'',''api_key'',''system''))', 'CHECK (actor_type IN (''admin'',''api_key'',''system'',''agent''))')
 WHERE type = 'table' AND name = 'audit_log';
PRAGMA writable_schema = OFF;
