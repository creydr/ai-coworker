CREATE TABLE IF NOT EXISTS threads (
    id         TEXT PRIMARY KEY,
    channel    TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    thread_ts  TEXT NOT NULL,
    repo       TEXT NOT NULL DEFAULT '',
    issue_num  INTEGER NOT NULL DEFAULT 0,
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel, channel_id, thread_ts)
);

CREATE TABLE IF NOT EXISTS messages (
    id         TEXT PRIMARY KEY,
    thread_id  TEXT NOT NULL REFERENCES threads(id),
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_messages_thread_created
    ON messages (thread_id, created_at);

CREATE TABLE IF NOT EXISTS tasks (
    id         TEXT PRIMARY KEY,
    thread_id  TEXT NOT NULL REFERENCES threads(id),
    intent     TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending',
    input      TEXT NOT NULL DEFAULT '',
    result     TEXT NOT NULL DEFAULT '',
    worker_id  TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tasks_status_created
    ON tasks (status, created_at);
