package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"floor/internal/command"
	"floor/internal/llm"
	"floor/internal/metrics"
	"floor/internal/money"
	"floor/internal/render"
)

const (
	callbackMoney = "$:" // $:ok, $:no — подтверждение сверки, msgID из нажатия

	kvMoneyBad      = "money_bad_hash"
	kvMoneyChecks   = "money_checks_hash"
	kvDebtClosed    = "money_debt_closed"
	kvTransfersDone = "money_transfers_ended"
	kvMoneyReminded = "money_reminded_for"
)

// currentMoney читает план. При ошибке остаётся последняя рабочая версия и один раз
// на версию файла приходит сообщение. false — рабочей версии ещё не было.
func (a *App) currentMoney(ctx context.Context, today string) (money.File, bool) {
	path := a.conf().MoneyFile
	if path == "" {
		return money.File{}, false
	}
	raw, f, err := money.Load(path, today)
	if err == nil {
		a.mu.Lock()
		a.money, a.moneyOK = f, true
		a.mu.Unlock()
		return f, true
	}
	hash := shortHash(string(raw) + err.Error())
	if seen, _ := a.Store.GetKV(ctx, kvMoneyBad); seen != hash {
		text := "💰 В " + escape(path) + " ошибка, считаю по прошлой версии:\n" + escape(err.Error())
		if _, sendErr := a.say(ctx, kindAlert, text, nil); sendErr == nil {
			_ = a.Store.SetKV(ctx, kvMoneyBad, hash)
		}
	}
	a.Log.Warn("money_invalid", "error", err)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.money, a.moneyOK
}

// lastSnapshot — последняя сверка; при первом запуске пишет точку отсчёта из файла.
func (a *App) lastSnapshot(ctx context.Context, f money.File) (*money.Snapshot, error) {
	s, err := a.Store.LastSnapshot(ctx)
	if err != nil || s != nil {
		return s, err
	}
	base := f.BaselineSnapshot()
	if err := a.Store.InsertSnapshot(ctx, base, a.Now()); err != nil {
		return nil, err
	}
	return &base, nil
}

// moneyTick — проверки плана, события и напоминание о сверке.
func (a *App) moneyTick(ctx context.Context, local time.Time) error {
	today := local.Format(time.DateOnly)
	f, ok := a.currentMoney(ctx, today)
	if !ok {
		return nil
	}
	last, err := a.lastSnapshot(ctx, f)
	if err != nil {
		return err
	}

	// Дыры в плане — один раз на версию файла.
	raw, _ := money.Encode(f)
	hash := shortHash(string(raw))
	seen, err := a.Store.GetKV(ctx, kvMoneyChecks)
	if err != nil {
		return err
	}
	if seen != hash {
		if text := render.MoneyWarnings(f.Checks(), f); text != "" {
			if _, err := a.say(ctx, kindReport, text, nil); err != nil {
				return fmt.Errorf("план денег: %w", err)
			}
		}
		if err := a.Store.SetKV(ctx, kvMoneyChecks, hash); err != nil {
			return err
		}
	}

	if end := f.TransfersEnd(); end != "" && today > end {
		if done, _ := a.Store.GetKV(ctx, kvTransfersDone); done != end {
			if _, err := a.say(ctx, kindReport, render.TransfersEnded(f.MonthlyTransfers("", end)), nil); err != nil {
				return fmt.Errorf("переводы: %w", err)
			}
			if err := a.Store.SetKV(ctx, kvTransfersDone, end); err != nil {
				return err
			}
		}
	}
	return a.moneyRemind(ctx, local, f, last)
}

// moneyRemind — нет сверки MONEY_REMIND_DAYS дней: на следующий день после ближайшего
// перевода, в 12:00, одна строка без кнопок. Один раз на период.
func (a *App) moneyRemind(ctx context.Context, local time.Time, f money.File, last *money.Snapshot) error {
	if last == nil || local.Hour() < 12 {
		return nil
	}
	due := metrics.AddDays(last.Date, a.conf().MoneyRemindDays)
	occ := f.Occurrences("", metrics.AddDays(due, -1), metrics.AddDays(due, 62))
	if len(occ) == 0 {
		return nil
	}
	if local.Format(time.DateOnly) < metrics.AddDays(occ[0].Date, 1) {
		return nil
	}
	if done, _ := a.Store.GetKV(ctx, kvMoneyReminded); done == last.Date {
		return nil
	}
	if _, err := a.say(ctx, kindRemind, render.MoneyRemind, nil); err != nil {
		return fmt.Errorf("напоминание: %w", err)
	}
	return a.Store.SetKV(ctx, kvMoneyReminded, last.Date)
}

type pendingMoney struct {
	Date   string           `json:"date"`
	Values map[string]int64 `json:"values"`
}

// onMoneyText — сообщение похоже на сверку: разобрать и спросить подтверждение.
// false — это не сверка, дальше обычный свободный текст.
func (a *App) onMoneyText(ctx context.Context, text string) bool {
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	f, ok := a.currentMoney(ctx, today)
	if !ok {
		return false
	}
	p := f.ParseText(text)
	if !p.IsReconcile() {
		return false
	}
	// В историю диалога — только отметка, сами суммы там не нужны.
	if err := a.Store.AddMessage(ctx, "user", "💰 сверка от "+render.DayMonth(today), a.Now()); err != nil {
		a.Log.Warn("message_save_failed", "error", err)
	}
	values := p.Values
	if p.NeedsHelp() {
		values = a.parseMoneyLLM(ctx, today, f, text)
		if values == nil {
			a.send(ctx, kindNotice, render.MoneyUnclear)
			return true
		}
	}
	last, err := a.lastSnapshot(ctx, f)
	if err != nil {
		a.Log.Error("money_failed", "error", err)
		a.send(ctx, kindNotice, "Не получилось, попробуй ещё раз.")
		return true
	}
	snap := money.Merge(today, values, last, f)
	prevDate := today
	if last != nil {
		prevDate = last.Date
	}
	rows := [][]Button{{{Text: "Верно", Data: callbackMoney + "ok"}, {Text: "Отмена", Data: callbackMoney + "no"}}}
	id, err := a.sayRows(ctx, kindPrompt, render.MoneyConfirm(snap, f, prevDate), rows)
	if err != nil {
		a.Log.Warn("reply_failed", "error", err)
		return true
	}
	raw, _ := json.Marshal(pendingMoney{Date: today, Values: values})
	if err := a.Store.SetPending(ctx, id, string(raw), a.Now()); err != nil {
		a.Log.Error("money_failed", "error", err)
	}
	a.Log.Info("money_parsed", "values", len(values), "llm", p.NeedsHelp())
	return true
}

func (a *App) send(ctx context.Context, kind, text string) {
	if _, err := a.say(ctx, kind, text, nil); err != nil {
		a.Log.Warn("reply_failed", "error", err)
	}
}

// parseMoneyLLM — разбор сверки Claude: только JSON, проверяется кодом. nil — не вышло.
func (a *App) parseMoneyLLM(ctx context.Context, today string, f money.File, text string) map[string]int64 {
	raw, ok := a.ask(ctx, today, llm.MoneyParsePrompt(money.Scrub(text), f.Keys()), "money_parse")
	if !ok {
		return nil
	}
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return nil
	}
	var parsed map[string]*int64
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return nil
	}
	values := map[string]int64{}
	for k, v := range parsed {
		if v != nil {
			values[k] = *v
		}
	}
	if !f.Valid(values) {
		return nil
	}
	return values
}

// onMoney — «Верно» или «Отмена» под сверкой.
func (a *App) onMoney(ctx context.Context, action string, msgID int) string {
	payload, ok, err := a.Store.TakePending(ctx, msgID, a.Now())
	if err != nil {
		a.Log.Error("money_failed", "error", err)
		return "Не сохранилось, нажми ещё раз."
	}
	if !ok {
		a.edit(ctx, msgID, render.ExpiredReply, nil)
		return render.ExpiredReply
	}
	if action != "ok" {
		a.edit(ctx, msgID, "Сверка отменена.", nil)
		return ""
	}
	var p pendingMoney
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return ""
	}
	f, fok := a.currentMoney(ctx, p.Date)
	if !fok {
		return ""
	}
	last, err := a.lastSnapshot(ctx, f)
	if err != nil {
		a.Log.Error("money_failed", "error", err)
		return "Не сохранилось, нажми ещё раз."
	}
	snap := money.Merge(p.Date, p.Values, last, f)
	if err := a.Store.InsertSnapshot(ctx, snap, a.Now()); err != nil {
		a.Log.Error("money_failed", "error", err)
		return "Не сохранилось, нажми ещё раз."
	}
	prevDate := p.Date
	if last != nil {
		prevDate = last.Date
	}
	a.edit(ctx, msgID, render.MoneyConfirm(snap, f, prevDate)+"\n\nСохранено ✅", nil)
	a.Log.Info("money_saved", "date", p.Date)
	a.dropKind(ctx, kindRemind, 0)
	// Отчёт с последней строкой от Claude: до минуты, кнопка столько не ждёт.
	go a.moneyReport(context.WithoutCancel(ctx), f, snap, last)
	return ""
}

func (a *App) moneyReport(ctx context.Context, f money.File, snap money.Snapshot, prev *money.Snapshot) {
	r := f.Calc(snap, prev)
	line := render.MoneyFallback(r)
	if r.Worst() != money.OnTrack || !r.Life.OK {
		if raw, ok := a.ask(ctx, snap.Date, llm.MoneyPrompt(render.MoneyContext(r, f)), "money"); ok {
			if clean, ok := llm.CleanBottleneck(raw); ok {
				line = clean
			}
		}
	} else {
		line = ""
	}
	a.send(ctx, kindReport, render.Money(r, f, line))
	defer a.sendChart(ctx, kindReport, command.ChartMoney, snap.Date) // после всех текстов: рисуется дольше

	if r.Debt != nil && r.Debt.Closed {
		if done, _ := a.Store.GetKV(ctx, kvDebtClosed); done == "" {
			a.send(ctx, kindReport, render.DebtClosed(r, f, f.MonthlyTransfers(f.ByRole(money.RoleDebtFund), snap.Date)))
			if err := a.Store.SetKV(ctx, kvDebtClosed, snap.Date); err != nil {
				a.Log.Warn("money_failed", "error", err)
			}
		}
	}
}

// lastReport — расчёт по последней сверке (для недели и Claude). nil — сверок ещё не было.
func (a *App) lastReport(ctx context.Context, today string) (*money.Report, money.File) {
	f, ok := a.currentMoney(ctx, today)
	if !ok {
		return nil, f
	}
	last, err := a.Store.LastSnapshot(ctx)
	if err != nil || last == nil || last.Baseline {
		return nil, f
	}
	r := f.Calc(*last, nil)
	return &r, f
}

// planReport — как lastReport, но без сверок считает от точки отсчёта: Claude должен
// видеть план с первого дня. baseline = true — это ещё не сверка.
func (a *App) planReport(ctx context.Context, today string) (*money.Report, money.File, bool) {
	if r, f := a.lastReport(ctx, today); r != nil {
		return r, f, false
	}
	f, ok := a.currentMoney(ctx, today)
	if !ok {
		return nil, f, false
	}
	r := f.Calc(f.BaselineSnapshot(), nil)
	return &r, f, true
}

// moneyGoalToday — строка утра: сегодня наступила цель-дата.
func (a *App) moneyGoalToday(ctx context.Context, today string) string {
	f, ok := a.currentMoney(ctx, today)
	if !ok {
		return ""
	}
	var lines []string
	for _, g := range f.Goals {
		if g.Kind == money.GoalDate && g.On == today {
			lines = append(lines, "🎯 Сегодня: "+escape(g.Title))
		}
	}
	return strings.Join(lines, "\n")
}
