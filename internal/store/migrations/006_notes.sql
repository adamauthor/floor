-- Заметки владельца («запомни, что…»): попадают в контекст Claude.
CREATE TABLE notes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    text       TEXT NOT NULL
);

-- Изменения из чата, ждущие «Да».
CREATE TABLE pending_actions (
    msg_id     INTEGER PRIMARY KEY,
    created_at TEXT NOT NULL,
    payload    TEXT NOT NULL
);
