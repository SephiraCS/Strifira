-- Phase 36: shares are identified by their presenter, not a free-text
-- title (product direction 2026-09-17: wall of videos, presenter-named).
ALTER TABLE screen_shares DROP COLUMN title;
