-- Multipart resumption (stored part size) and namespace-scoped API keys.
ALTER TABLE uploads ADD COLUMN part_size INTEGER;
ALTER TABLE api_keys ADD COLUMN namespaces_json TEXT;
