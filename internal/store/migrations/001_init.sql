CREATE TABLE days (
    date            TEXT PRIMARY KEY,
    tz_offset       TEXT NOT NULL,
    steps           INTEGER,
    sleep_minutes   INTEGER,
    wake_at         TEXT,
    wake_delta_min  INTEGER,
    floor_status    TEXT NOT NULL DEFAULT 'none'
                    CHECK (floor_status IN ('yes','no','none')),
    morning_sent_at TEXT,
    evening_sent_at TEXT,
    evening_msg_id  INTEGER,
    updated_at      TEXT NOT NULL
);

CREATE TABLE exports (
    id           INTEGER PRIMARY KEY,
    received_at  TEXT NOT NULL,
    sha256       TEXT NOT NULL UNIQUE,
    payload      TEXT NOT NULL
);
CREATE INDEX exports_received_at ON exports (received_at);

CREATE TABLE messages (
    id          INTEGER PRIMARY KEY,
    at          TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('user','assistant')),
    text        TEXT NOT NULL
);

CREATE TABLE kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
