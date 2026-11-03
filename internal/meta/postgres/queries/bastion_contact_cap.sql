-- The bound on the contact rows of one registration (R-BAS-010; work package 2). The name of a replica is what the
-- Bastion reports, so a holder of the Bastion token could create a row per name; the writer keeps the newest rows of
-- a registration and deletes the rest after every write. A real registration has as many rows as replicas.

-- name: BastionContactTrim :execrows
DELETE FROM bastion_contact
 WHERE bastion_id = sqlc.arg(bastion_id)::text::uuid
   AND replica IN (SELECT c.replica FROM bastion_contact c
                    WHERE c.bastion_id = sqlc.arg(bastion_id)::text::uuid
                    ORDER BY c.contact_at DESC, c.replica
                   OFFSET sqlc.arg(keep)::int);
