ALTER TABLE threads ADD COLUMN IF NOT EXISTS thread_key TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN IF NOT EXISTS properties JSONB NOT NULL DEFAULT '{}';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'threads' AND column_name = 'channel_id') THEN
        UPDATE threads SET thread_key = channel_id || '/' || thread_ts WHERE thread_key = '';

        UPDATE threads SET properties = jsonb_build_object('repo', repo, 'issue_num', issue_num::text)
        WHERE channel = 'github' AND repo != '' AND properties = '{}';

        UPDATE threads SET properties = jsonb_build_object('channel_id', channel_id, 'thread_ts', thread_ts)
        WHERE channel = 'slack' AND properties = '{}';

        ALTER TABLE threads DROP COLUMN channel_id;
        ALTER TABLE threads DROP COLUMN thread_ts;
        ALTER TABLE threads DROP COLUMN repo;
        ALTER TABLE threads DROP COLUMN issue_num;
    END IF;
END $$;

DROP INDEX IF EXISTS threads_channel_channel_id_thread_ts_key;
CREATE UNIQUE INDEX IF NOT EXISTS threads_channel_thread_key_key ON threads (channel, thread_key);
