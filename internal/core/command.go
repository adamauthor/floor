package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"floor/internal/command"
	"floor/internal/metrics"
	"floor/internal/money"
	"floor/internal/render"
	"floor/internal/schedule"
	"floor/internal/store"
)

// Изменения из чата: «сделай пол улицы — выйти на балкон», «запомни, что 15-го премия».
// Claude переводит просьбу в действия (internal/command), код проверяет и показывает
// «было → стало», применяет только после «Да». Заметки применяются сразу.

const callbackDo = "do:" // do:ok, do:no — ответ на «Сделать так?»

// commandState — текущее состояние, к которому применяются действия.
func (a *App) commandState(ctx context.Context) (command.State, error) {
	local := a.Now().In(a.location(ctx))
	today := local.Format(time.DateOnly)
	s := command.State{
		Today:    today,
		Habits:   a.currentHabits(ctx, today),
		Settings: a.conf().Settings(),
		Notes:    map[int]string{},
		Floor:    func(date string) bool { return !schedule.FloorExpired(local, date) },
	}
	if f, ok := a.currentMoney(ctx, today); ok {
		s.Money = &f
	}
	notes, err := a.Store.Notes(ctx)
	if err != nil {
		return s, err
	}
	for _, n := range notes {
		s.Notes[n.ID] = n.Text
	}
	return s, nil
}

// controlContext — блок «Что можно менять» для Claude.
func (a *App) controlContext(ctx context.Context) string {
	s, err := a.commandState(ctx)
	if err != nil {
		a.Log.Warn("context_failed", "error", err)
		return ""
	}
	return render.ControlContext(s.Habits, s.Settings, s.Money)
}

// notesContext — заметки владельца для любого промпта.
func (a *App) notesContext(ctx context.Context) string {
	notes, err := a.Store.Notes(ctx)
	if err != nil || len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nЗаметки Адама (что он просил запомнить):\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "#%d (%s): %s\n", n.ID, n.At.In(a.location(ctx)).Format(time.DateOnly), n.Text)
	}
	return b.String()
}

// replyWithActions — ответ Claude с возможными действиями: отправить текст и, если есть,
// предложить изменения. Возвращает, что записать в историю диалога.
func (a *App) replyWithActions(ctx context.Context, raw string) (string, error) {
	reply, actions, err := command.Split(raw)
	reply = truncateReply(reply)
	var charts []command.Action
	if err == nil {
		charts, actions, err = command.Charts(actions, a.Now().In(a.location(ctx)).Format(time.DateOnly))
	}
	if err == nil && len(actions) == 0 {
		var sendErr error
		if reply != "" || len(charts) == 0 {
			_, sendErr = a.say(ctx, kindChat, escape(reply), nil)
		}
		history := reply
		for _, c := range charts {
			a.sendChart(ctx, kindChat, c.What, c.Date)
			history += "\n(показан график: " + c.What + ")"
		}
		return history, sendErr
	}
	defer func() {
		for _, c := range charts {
			a.sendChart(ctx, kindChat, c.What, c.Date)
		}
	}()
	var plan command.Plan
	if err == nil {
		var st command.State
		if st, err = a.commandState(ctx); err == nil {
			plan, err = command.Build(st, actions)
		}
	}
	if err != nil {
		a.Log.Warn("command_rejected", "error", err)
		_, sendErr := a.say(ctx, kindChat, render.ActionsRejected(reply, err), nil)
		return reply + "\n(изменение не прошло проверку: " + err.Error() + ")", sendErr
	}
	history := reply + "\n(предложено: " + strings.Join(plan.Lines, "; ") + ")"
	if !command.NeedsConfirm(actions) {
		if err := a.commit(ctx, plan); err != nil {
			a.Log.Error("command_failed", "error", err)
			_, sendErr := a.say(ctx, kindChat, render.ActionsRejected(reply, err), nil)
			return history, sendErr
		}
		text := render.ActionsDone(plan.Lines)
		if reply != "" {
			text = escape(reply) + "\n\n" + text
		}
		_, sendErr := a.say(ctx, kindChat, text, nil)
		return reply + "\n(применено: " + strings.Join(plan.Lines, "; ") + ")", sendErr
	}
	rows := [][]Button{{{Text: "Да", Data: callbackDo + "ok"}, {Text: "Нет", Data: callbackDo + "no"}}}
	id, sendErr := a.sayRows(ctx, kindPrompt, render.ActionsConfirm(reply, plan.Lines), rows)
	if sendErr != nil {
		return history, sendErr
	}
	if err := a.Store.SetAction(ctx, id, command.Encode(actions), a.Now()); err != nil {
		a.Log.Error("command_failed", "error", err)
	}
	a.Log.Info("command_proposed", "actions", len(actions))
	return history, nil
}

func truncateReply(s string) string {
	if r := []rune(s); len(r) > 800 {
		return string(r[:799]) + "…"
	}
	return s
}

// onDo — ответ на «Сделать так?».
func (a *App) onDo(ctx context.Context, answer string, msgID int) string {
	payload, ok, err := a.Store.TakeAction(ctx, msgID, a.Now())
	if err != nil {
		a.Log.Error("command_failed", "error", err)
		return "Не получилось, попробуй ещё раз."
	}
	if !ok {
		a.edit(ctx, msgID, render.ExpiredReply, nil)
		return render.ExpiredReply
	}
	if answer != "ok" {
		a.edit(ctx, msgID, "Не меняю.", nil)
		a.addHistory(ctx, "Адам отказался от изменений.")
		return ""
	}
	actions, err := command.Decode(payload)
	var plan command.Plan
	if err == nil {
		var st command.State
		if st, err = a.commandState(ctx); err == nil {
			plan, err = command.Build(st, actions) // состояние могло поменяться, пока ждали
		}
	}
	if err == nil {
		err = a.commit(ctx, plan)
	}
	if err != nil {
		a.Log.Warn("command_rejected", "error", err)
		a.edit(ctx, msgID, render.ActionsRejected("", err), nil)
		return ""
	}
	a.Log.Info("command_applied", "actions", len(actions))
	a.edit(ctx, msgID, render.ActionsDone(plan.Lines), nil)
	a.addHistory(ctx, "Применено: "+strings.Join(plan.Lines, "; "))
	a.Tick(ctx, a.Now()) // карточка, кнопка меню и расписание сразу видят новое
	return ""
}

func (a *App) addHistory(ctx context.Context, text string) {
	if err := a.Store.AddMessage(ctx, "assistant", text, a.Now()); err != nil {
		a.Log.Warn("message_save_failed", "error", err)
	}
}

// commit применяет проверенный план.
func (a *App) commit(ctx context.Context, p command.Plan) error {
	local := a.Now().In(a.location(ctx))
	cur := a.conf()
	if p.HabitsChange || p.Settings != cur.Settings() {
		next := cur
		if err := p.Settings.Apply(&next); err != nil {
			return err
		}
		a.tickMu.Lock()
		_, err := a.commitSettings(ctx, next, p.Habits)
		a.tickMu.Unlock()
		if err != nil {
			return err
		}
	}
	if p.MoneyChange {
		raw, err := money.Encode(*p.Money)
		if err != nil {
			return err
		}
		if err := os.WriteFile(cur.MoneyFile, raw, 0o600); err != nil {
			return fmt.Errorf("запись плана денег: %w", err)
		}
		a.currentMoney(ctx, local.Format(time.DateOnly))
	}
	if len(p.Marks) > 0 {
		if err := a.commitMarks(ctx, local, p.Marks); err != nil {
			return err
		}
	}
	for _, date := range p.Workouts {
		if err := a.manualWorkout(ctx, date, local); err != nil {
			return err
		}
	}
	var errs []error
	for _, text := range p.Remember {
		errs = append(errs, a.Store.AddNote(ctx, text, a.Now()))
	}
	for _, id := range p.Forget {
		errs = append(errs, a.Store.DeleteNote(ctx, id))
	}
	return errors.Join(errs...)
}

// commitMarks пишет отметки; если за день отмечено всё — ставит пол дня, как кнопка «Готово».
func (a *App) commitMarks(ctx context.Context, local time.Time, marks map[string]map[string]int) error {
	a.markMu.Lock()
	defer a.markMu.Unlock()
	for date, m := range marks {
		for id, level := range m {
			if err := a.Store.SetMark(ctx, date, id, level, local); err != nil {
				return err
			}
		}
		all, err := a.Store.MarksRange(ctx, date, date)
		if err != nil {
			return err
		}
		status := metrics.StatusYes
		for _, h := range a.currentHabits(ctx, date).Daily(date) {
			level, ok := all[date][h.ID]
			if !ok {
				status = ""
				break
			}
			if level == store.LevelMiss {
				status = metrics.StatusNo
			}
		}
		if status != "" {
			if err := a.Store.SetFloor(ctx, date, local.Format("-07:00"), status, local); err != nil {
				return err
			}
		}
	}
	return nil
}
