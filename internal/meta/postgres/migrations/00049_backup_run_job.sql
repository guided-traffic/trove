-- trove:phase expand
--
-- The Kubernetes Job a backup run belongs to (R-BAK-010, Q-BAK-44). The chart passes the pod's job-name label to the backup
-- pod; a retry pod of the Job that finds an active run of the same Job knows that run's pod is gone (a Job runs one pod at a
-- time) and waits for its lease instead of refusing with exit 3. NULL for a run without a Job: a manual run of the command,
-- the delete and verify runs of an operator's shell.
ALTER TABLE backup_run ADD COLUMN job_name text;
