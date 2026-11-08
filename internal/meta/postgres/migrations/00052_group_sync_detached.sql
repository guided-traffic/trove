-- trove:phase expand
--
-- A group renamed after a restore point is detached from group synchronization by the replay of its journal entry instead of
-- being renamed (Q-AUTH-206): a detached group is neither filled nor created by a login's group claim (R-AUTH-008). Only a
-- replay sets the flag, only an administrator clears it (R-AUTH-018).
ALTER TABLE user_group ADD COLUMN sync_detached boolean NOT NULL DEFAULT false;
