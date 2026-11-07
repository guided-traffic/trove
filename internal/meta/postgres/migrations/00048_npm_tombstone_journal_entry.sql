-- trove:phase expand
--
-- The tombstone a restore's journal replay writes names the journal entry that withheld the version
-- (R-NPM-047, R-NPM-044): `trove admin npm reinstate` copies it into the npm.reinstate event, so the audit
-- stream ties the reversal to the withdrawal it reverses. Null for every other source.

ALTER TABLE npm_tombstones
    ADD COLUMN journal_entry uuid,
    ADD CONSTRAINT npm_tombstones_journal_entry CHECK (journal_entry IS NULL OR source = 'journal');
