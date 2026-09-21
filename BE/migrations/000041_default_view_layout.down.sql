ALTER TABLE user_preferences
    DROP CONSTRAINT IF EXISTS user_preferences_default_view_layout_check,
    DROP COLUMN IF EXISTS default_view_layout;
