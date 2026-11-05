-- Журнал сообщений в чате: что прислал бот (и владелец), когда убрать из чата.
-- Сами данные дня, отметок, денег и тренировок лежат в своих таблицах — чат только витрина.
CREATE TABLE chat_messages (
    msg_id    INTEGER PRIMARY KEY,
    kind      TEXT NOT NULL,
    sent_at   TEXT NOT NULL,          -- UTC, RFC 3339
    delete_at TEXT,                   -- UTC; NULL — не убирать
    deleted   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX chat_messages_due ON chat_messages (deleted, delete_at);
CREATE INDEX chat_messages_kind ON chat_messages (kind, deleted);
