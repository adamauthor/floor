// Package internal — корень внутренних пакетов Floor.
//
// Карта пакетов (docs/SPEC.md, раздел 5.1):
//
//	config    env → Config                          (готово)
//	health    контракт выгрузки, FileSource/HTTPSource (контракт готов, M1)
//	metrics   сон → подъём, шаги, пол — чистые функции (M1)
//	store     SQLite                                   (M1)
//	render    тексты сообщений — чистые функции        (M2)
//	schedule  расписание по местному времени           (M2)
//	bot       Telegram                                 (M2)
//	llm       claude -p + fallback                     (интерфейс готов, M3)
package internal
