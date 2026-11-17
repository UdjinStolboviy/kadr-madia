-- kadr:bump-schema-version
-- Documents: allow kind = 'file' on presets, blobs and assets. SQLite cannot
-- alter a CHECK constraint; because no stored data changes, the documented
-- writable_schema procedure (https://sqlite.org/lang_altertable.html, "Making
-- Other Kinds Of Table Schema Changes") is used. The migrator bumps
-- schema_version afterwards (marker above) so every connection reloads it.
PRAGMA writable_schema = ON;
UPDATE sqlite_master
   SET sql = replace(sql, 'CHECK (kind IN (''image'',''video''))', 'CHECK (kind IN (''image'',''video'',''file''))')
 WHERE type = 'table' AND name IN ('presets', 'blobs', 'assets');
PRAGMA writable_schema = OFF;
