-- Record which encoder produced a rendition (video: h264_vaapi or libx264),
-- so a silent hardware → software fallback is visible after the fact.
ALTER TABLE renditions ADD COLUMN encoder TEXT;
