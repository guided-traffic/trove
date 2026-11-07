-- trove:phase expand
--
-- The bounds of a CI issuer and a mapping that the API and the configuration enforce, in the schema as well (Q-CI-69): a
-- clock skew is not negative, which would move the restore cut-off of R-CI-018 before restored_at, and a lifetime bound is
-- positive or NULL, the unbounded value. The write paths store a bound of 0 or less as NULL and validate the skew, so no row
-- they wrote breaks a constraint; a statement that would, a replayed or a hand-written one, fails its transaction.
ALTER TABLE ci_issuers ADD CONSTRAINT ci_issuers_clock_skew_not_negative CHECK (clock_skew >= interval '0 seconds');
ALTER TABLE ci_issuers ADD CONSTRAINT ci_issuers_max_ttl_positive CHECK (max_ttl > interval '0 seconds');
ALTER TABLE ci_mappings ADD CONSTRAINT ci_mappings_max_ttl_positive CHECK (max_ttl > interval '0 seconds');
