-- trove:phase expand
--
-- The import tasks of the Ansible Galaxy format (R-ANS-009, Q-ANS-8): a publish is processed in the request, so a task is final when it
-- is created, and `ansible-galaxy collection publish` reads it back from any replica. The id is a random UUID and unguessable. A task
-- is kept for ansible.import_task_keep and then deleted by the periodic task ansible-import-task-sweep; the version stays.
CREATE TABLE ansible_import_tasks (
    id                uuid        PRIMARY KEY,
    repo_id           bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name              text        COLLATE "C" NOT NULL,
    version           text        COLLATE "C" NOT NULL,
    state             text        NOT NULL CHECK (state IN ('completed', 'failed')),
    error_code        text,
    error_description text,
    messages          jsonb       NOT NULL DEFAULT '[]',
    created_by        text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz NOT NULL DEFAULT now()
);

-- The task list of a repository, newest first, and the sweep's range delete.
CREATE INDEX ansible_import_tasks_by_repo ON ansible_import_tasks (repo_id, created_at DESC);
CREATE INDEX ansible_import_tasks_by_age ON ansible_import_tasks (created_at);
