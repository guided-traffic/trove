-- trove:phase expand
--
-- The trove Bastion (R-BAS-004, R-HA-018): the checks that 00032 and 00033 added NOT VALID are validated
-- here, under a lock that lets reads and writes through.

ALTER TABLE principal VALIDATE CONSTRAINT principal_bastion_name;
ALTER TABLE principal VALIDATE CONSTRAINT principal_kind_check;
ALTER TABLE api_token VALIDATE CONSTRAINT api_token_type_check;
