-- The time of the first change of a suite that no build has published yet, so that the delay from a change to the commit of the
-- generation that lists it can be measured (R-DEB-010, trove_deb_index_delay_seconds). A suite is dirty from its creation; a raise keeps
-- the earlier time; a published generation clears it, or sets it to now when a change arrived while the build ran.

ALTER TABLE deb_suites ADD COLUMN dirty_since timestamptz;
UPDATE deb_suites SET dirty_since = now() WHERE change_seq > built_seq;
