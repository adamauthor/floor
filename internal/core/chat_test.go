package core

import (
	"errors"
	"testing"
	"time"
)

func lastID(t *testing.T, h *harness, sub string) int {
	t.Helper()
	m := h.tg.matching(sub)
	if len(m) == 0 {
		t.Fatalf("нет сообщения с %q", sub)
	}
	return m[len(m)-1].id
}

func TestChatCleanupDaily(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 10:30", plus5)
	card := lastID(t, h, "📌")
	morning1 := lastID(t, h, "☀️")
	h.tick("2026-10-31 21:00", plus5)
	evening1 := lastID(t, h, "🌙")

	// Следующее утро убирает прошлое утро; вечер ещё нужен до 12:00.
	h.tick("2026-11-01 10:30", plus5)
	morning2 := lastID(t, h, "☀️")
	h.tick("2026-11-01 10:31", plus5)
	if !h.tg.isDeleted(morning1) || h.tg.isDeleted(morning2) || h.tg.isDeleted(evening1) {
		t.Errorf("утро: удалено %v", h.tg.deleted)
	}
	// Следующий вечер убирает прошлый.
	h.tick("2026-11-01 21:00", plus5)
	h.tick("2026-11-01 21:01", plus5)
	if !h.tg.isDeleted(evening1) || h.tg.isDeleted(lastID(t, h, "🌙")) {
		t.Errorf("вечер: удалено %v", h.tg.deleted)
	}
	// Сводка недели и карточка остаются.
	h.tick("2026-11-01 20:00", plus5)
	weekly := lastID(t, h, "📅")
	h.tick("2026-11-03 10:30", plus5)
	if h.tg.isDeleted(card) || h.tg.isDeleted(weekly) {
		t.Errorf("карточка или неделя удалены: %v", h.tg.deleted)
	}
}

func TestChatCleanupMoneyAndChat(t *testing.T) {
	h := newHarness(t)
	h.withMoney(t)
	h.tick("2026-12-20 12:00", plus5)

	h.app.OnText(WithIncoming(ctx, 9001), "карта 100к, лайв 20 000, дф 550к, подушка 300к, долг 700 000")
	confirm := h.tg.matching("Сверка на 20 декабря")[0]
	h.tick("2026-12-20 12:01", plus5)
	if !h.tg.isDeleted(9001) {
		t.Error("сообщение с остатками не убрано")
	}
	h.app.OnCallback(ctx, confirm.rows[0][0].Data, confirm.id)
	report := h.waitText(t, "💰 <b>Деньги")

	h.llm.reply = "Ответ."
	h.app.OnText(WithIncoming(ctx, 9002), "как дела?")
	reply := lastID(t, h, "Ответ.")
	h.app.OnSettings(WithIncoming(ctx, 9003))
	hint := lastID(t, h, "слева от поля ввода")

	h.tick("2026-12-20 12:05", plus5)
	if !h.tg.isDeleted(9003) || h.tg.isDeleted(9002) || h.tg.isDeleted(confirm.id) {
		t.Errorf("через минуту: %v", h.tg.deleted)
	}
	h.tick("2026-12-20 13:10", plus5)
	if !h.tg.isDeleted(hint) || h.tg.isDeleted(reply) {
		t.Errorf("через час: %v", h.tg.deleted)
	}
	h.tick("2026-12-21 12:10", plus5)
	for _, id := range []int{9002, reply, confirm.id} {
		if !h.tg.isDeleted(id) {
			t.Errorf("через сутки не убрано %d: %v", id, h.tg.deleted)
		}
	}
	if h.tg.isDeleted(report.id) {
		t.Error("денежный отчёт убран")
	}
}

func TestChatSweepTelegramLimits(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-11-01 10:00", plus5)
	h.app.track(ctx, 501, kindAlert)
	h.app.track(ctx, 502, kindNotice)

	// Telegram недоступен: ничего не помечается, попытка повторится.
	h.tg.delErr = errors.New("сеть")
	h.app.sweep(ctx, at("2026-11-01 11:30", plus5))
	h.tg.delErr = nil
	h.app.sweep(ctx, at("2026-11-01 11:31", plus5))
	if !h.tg.isDeleted(502) {
		t.Error("после сбоя не повторилось")
	}
	// Удалённое уже не удаляется повторно.
	h.app.sweep(ctx, at("2026-11-01 11:32", plus5))
	if len(h.tg.deleted) != 1 {
		t.Errorf("удаления: %v", h.tg.deleted)
	}
	// Старше 48 часов Telegram не удаляет: только отметка в журнале.
	h.app.sweep(ctx, at("2026-11-01 10:00", plus5).Add(49*time.Hour))
	if h.tg.isDeleted(501) {
		t.Error("пытались удалить сообщение старше 48 часов")
	}
	if due, _ := h.db.DueMessages(ctx, at("2026-11-10 10:00", plus5), 10); len(due) != 0 {
		t.Errorf("в журнале остались: %+v", due)
	}
}
