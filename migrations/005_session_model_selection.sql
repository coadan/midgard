ALTER TABLE session_projection ADD COLUMN provider TEXT NOT NULL DEFAULT '';
ALTER TABLE session_projection ADD COLUMN profile TEXT NOT NULL DEFAULT '';
ALTER TABLE session_projection ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE session_projection ADD COLUMN effort TEXT NOT NULL DEFAULT '';
