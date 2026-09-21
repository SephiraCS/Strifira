ALTER TABLE user_preferences
    ADD COLUMN default_view_layout VARCHAR(10) NOT NULL DEFAULT 'list',
    ADD CONSTRAINT user_preferences_default_view_layout_check CHECK (default_view_layout IN ('list', 'board'));
