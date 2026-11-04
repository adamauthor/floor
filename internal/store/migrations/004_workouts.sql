-- Тренировки: из Google Health, запасных выгрузок или кнопки «Была без часов».
CREATE TABLE workouts (
    id       TEXT PRIMARY KEY,
    date     TEXT NOT NULL,
    start_at TEXT,
    minutes  INTEGER,
    type     TEXT,
    source   TEXT NOT NULL CHECK (source IN ('google','file','http','manual'))
);
CREATE INDEX workouts_date ON workouts (date);
