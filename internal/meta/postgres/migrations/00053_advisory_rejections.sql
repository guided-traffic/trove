-- Advisory records that a bound rejects (R-VULN-004, Q-VULN-116 to Q-VULN-123).
--
-- vuln_advisory.adapter_version: the version of the format adapter's normalization the row was written with
-- (match.AdapterVersion). The digest skip of a sync applies to a row only when this is the current version, so that a
-- row an earlier adapter wrote, and that a rejected record kept through a forced run, is written again when it next
-- passes. The sync writes every row with the version; the default 0 is no version of any adapter, so the first full sync after
-- this change writes every row once, as one after an adapter bump does.
ALTER TABLE vuln_advisory ADD COLUMN adapter_version integer NOT NULL DEFAULT 0;

-- vuln_source_state.full_rejected: the rejected records of the last full sync, kept apart from rejected (the last run of any
-- kind), so that the count on the sources screen does not fall to 0 with the next incremental run. record_limit: the
-- max_record_size the last full sync completed with; a configured limit that differs, after a sync that rejected records,
-- makes the next run read the archive again instead of answering "unchanged".
ALTER TABLE vuln_source_state ADD COLUMN full_rejected integer NOT NULL DEFAULT 0;
ALTER TABLE vuln_source_state ADD COLUMN record_limit bigint NOT NULL DEFAULT 0;
