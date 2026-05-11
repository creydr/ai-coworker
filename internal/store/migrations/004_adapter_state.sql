CREATE TABLE IF NOT EXISTS adapter_state (
    adapter TEXT NOT NULL,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (adapter, key)
);