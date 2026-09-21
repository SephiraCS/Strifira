-- Restore the LiveKit room column (nullable-unique: rows recreated without
-- a room cannot invent one; NULLs are exempt from the unique index).
ALTER TABLE screen_shares ADD COLUMN room_name TEXT;
CREATE UNIQUE INDEX idx_screen_shares_room_name ON screen_shares (room_name) WHERE room_name IS NOT NULL;
