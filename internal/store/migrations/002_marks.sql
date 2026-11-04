-- Отметки по привычкам: уровень дня для каждой (0 — пропуск, 1 — пол, 2 — цель).
CREATE TABLE habit_marks (
    date       TEXT NOT NULL,
    habit_id   TEXT NOT NULL,
    level      INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (date, habit_id)
);

-- Ответы на редкие вопросы: настроение недели, автоматизм и опросник раз в месяц.
-- period — понедельник недели или YYYY-MM, habit_id пуст, если вопрос не про привычку.
CREATE TABLE answers (
    kind     TEXT NOT NULL,
    period   TEXT NOT NULL,
    habit_id TEXT NOT NULL DEFAULT '',
    value    INTEGER NOT NULL,
    at       TEXT NOT NULL,
    PRIMARY KEY (kind, period, habit_id)
);
