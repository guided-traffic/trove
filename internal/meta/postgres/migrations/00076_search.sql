-- trove:phase expand
--
-- The suggestions of the search field (R-API-064, Q-API-274): two indexes over package names, without the extension pg_trgm
-- (00017). packages_name_lower serves a whole-name prefix in byte order; packages_name_words a prefix of a word inside a name,
-- where a word is a run of a to z and 0 to 9 of the lower-cased name, so `ui` finds `@acme/ui-kit` and `logging` finds
-- `microsoft.extensions.logging`.
--
-- The single-key shortcuts of the UI are on by default (R-API-021, Q-API-280). No shortcut existed before this step, so a stored
-- false turned nothing off: every stored row is set to true, and its revision moves with it, because the revision is the ETag of
-- the preferences (R-API-012).

ALTER TABLE packages
    ADD COLUMN name_words tsvector GENERATED ALWAYS AS
        (to_tsvector('simple'::regconfig, regexp_replace(lower(name), '[^a-z0-9]+', ' ', 'g'))) STORED;
CREATE INDEX packages_name_words ON packages USING gin (name_words);
CREATE INDEX packages_name_lower ON packages ((lower(name) COLLATE "C"));

ALTER TABLE user_preferences ALTER COLUMN keyboard_shortcuts SET DEFAULT true;
UPDATE user_preferences SET keyboard_shortcuts = true, revision = revision + 1, updated_at = now() WHERE NOT keyboard_shortcuts;
