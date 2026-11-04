-- Сверки денег: остатки по счетам и долг на местную дату (SPEC_v2, 2.9).
CREATE TABLE money_snapshots (
    id         INTEGER PRIMARY KEY,
    date       TEXT NOT NULL,
    at         TEXT NOT NULL,
    debt_total INTEGER,
    source     TEXT NOT NULL CHECK (source IN ('text','baseline'))
);
CREATE TABLE money_balances (
    snapshot_id INTEGER NOT NULL REFERENCES money_snapshots(id),
    account     TEXT NOT NULL,
    amount      INTEGER NOT NULL,
    carried     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (snapshot_id, account)
);
-- Распознанная, но не подтверждённая сверка; чистится через 24 часа.
CREATE TABLE money_pending (
    msg_id     INTEGER PRIMARY KEY,
    created_at TEXT NOT NULL,
    payload    TEXT NOT NULL
);
