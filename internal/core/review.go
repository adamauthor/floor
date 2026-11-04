package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"floor/internal/habits"
	"floor/internal/llm"
	"floor/internal/metrics"
	"floor/internal/render"
	"floor/internal/store"
)

const (
	kvMonthlySent = "monthly_sent"
	kvDataGap     = "data_gap_alerted"
	kvWatchToken  = "watch_token"
	kvWatchChat   = "watch_chat"
	kvWatchName   = "watch_name"

	answerMood   = "mood"
	answerAuto   = "auto"
	answerPHQ    = "phq"
	answerSocial = "social"

	watchPending = "…" // в кнопке меню: ссылка отправлена, человек её ещё не открыл

	dataGapDays = 3
	// releaseMinWeeks — раньше этого возраста привычку не выпускают: автоматизм
	// в первые недели переоценивается, а держится он месяцами (Lally et al. 2010).
	releaseMinWeeks = 5
)

// weekData собирает недельную сводку: отметки, возвращения и регулярность сна.
func (a *App) weekData(ctx context.Context, monday, today string, week []store.Day, f habits.File) (render.WeeklyData, error) {
	sunday := metrics.AddDays(monday, 6)
	marks, err := a.Store.MarksRange(ctx, metrics.AddDays(monday, -1), sunday)
	if err != nil {
		return render.WeeklyData{}, err
	}
	nights, err := a.Store.Days(ctx, metrics.AddDays(today, -7), today)
	if err != nil {
		return render.WeeklyData{}, err
	}
	data := render.WeeklyData{
		Monday: monday, Today: today, Days: plainDays(week), Habits: f.Active(),
		Wake: a.conf().WakeAnchor.String(), Marks: levels(marks),
		Returns: metrics.Returns(levels(marks), monday, sunday),
	}
	if sri, ok := metrics.SleepRegularity(plainDays(nights)); ok {
		data.SRI = &sri
	}
	data.Training = a.trainingDays(ctx, monday, sunday)
	if r, _ := a.lastReport(ctx, today); r != nil {
		data.Extra = append(data.Extra, render.MoneyLine(*r, today))
	}
	if social, err := a.Store.Answers(ctx, answerSocial, metrics.AddDays(monday, -7)); err == nil {
		if v, ok := social[""]; ok {
			data.Extra = append(data.Extra, render.SocialLine(v == 1))
		}
	}
	return data, nil
}

// ---------- настроение недели и разбор якоря ----------

// sendReview — после сводки одно сообщение по шагам: настроение, люди, якорь, финграмотность.
// Ни один вопрос не обязателен: в понедельник в 12:00 кнопки протухают.
func (a *App) sendReview(ctx context.Context, monday string, f habits.File) {
	rows := [][]Button{make([]Button, 0, 5)}
	for n := 1; n <= 5; n++ {
		rows[0] = append(rows[0], Button{Text: strconv.Itoa(n), Data: fmt.Sprintf("%s%s:mood:%d", callbackWeek, monday, n)})
	}
	if _, err := a.TG.SendRows(ctx, render.MoodQuestion, rows); err != nil {
		a.Log.Warn("review_send_failed", "error", err)
	}
}

func anchorRows(monday string, active []habits.Habit) [][]Button {
	rows := make([][]Button, 0, len(active)+1)
	for _, h := range active {
		rows = append(rows, []Button{{Text: render.HabitName(h), Data: callbackWeek + monday + ":a:" + h.ID}})
	}
	return append(rows, []Button{{Text: "Все сработали", Data: callbackWeek + monday + ":a:*"}})
}

// onReview — ответ на вопрос недели.
func (a *App) onReview(ctx context.Context, rest string, msgID int) string {
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		a.Log.Warn("callback_invalid")
		return ""
	}
	monday, kind, value := parts[0], parts[1], parts[2]
	now := a.Now()
	local := now.In(a.location(ctx))
	if reviewExpired(local, monday) {
		return render.ExpiredReply
	}
	f := a.currentHabits(ctx, local.Format(time.DateOnly))
	switch kind {
	case answerMood:
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 5 {
			return ""
		}
		if err := a.Store.SetAnswer(ctx, answerMood, monday, "", n, now); err != nil {
			a.Log.Error("answer_failed", "error", err)
			return "Не сохранилось, нажми ещё раз."
		}
		a.edit(ctx, msgID, render.SocialQuestion(n), [][]Button{{
			{Text: "Да", Data: callbackWeek + monday + ":s:1"}, {Text: "Нет", Data: callbackWeek + monday + ":s:0"},
		}})
	case "s":
		met := 0
		if value == "1" {
			met = 1
		}
		if err := a.Store.SetAnswer(ctx, answerSocial, monday, "", met, now); err != nil {
			a.Log.Error("answer_failed", "error", err)
			return "Не сохранилось, нажми ещё раз."
		}
		a.edit(ctx, msgID, render.AnchorQuestion(), anchorRows(monday, f.Active()))
	case "a":
		if value == "*" {
			text, rows := a.learningStep(ctx, monday, "Якоря на месте. Неделя — та же схема.")
			a.edit(ctx, msgID, text, rows)
			return ""
		}
		h, ok := f.Find(value)
		if !ok {
			return ""
		}
		a.edit(ctx, msgID, "Думаю над якорем «"+escape(h.Title)+"»…", nil)
		// Claude отвечает до минуты, а кнопка ждёт ответа секунды: дальше — в фоне.
		go a.reviewAnchor(context.WithoutCancel(ctx), msgID, monday, h)
	case "l":
		done := 0
		if value == "1" {
			done = 1
		}
		if err := a.Store.SetAnswer(ctx, answerLearning, monday, "", done, now); err != nil {
			a.Log.Error("answer_failed", "error", err)
			return "Не сохранилось, нажми ещё раз."
		}
		text := "📚 Действие недели сделано ✅"
		if done == 0 {
			text = "📚 Перенёс: тема повторится в понедельник, программа сдвинется на неделю."
			if w, ok := a.learningWeek(ctx, metrics.AddDays(monday, 7)); !ok || !w.Repeat {
				text = "📚 Тема закрыта как пропущенная: переносить можно не больше двух раз подряд."
			}
		}
		a.Log.Info("learning_answered", "week", monday, "done", done == 1)
		a.edit(ctx, msgID, text, nil)
	}
	return ""
}

// reviewExpired — вопросы недели живут до понедельника, 12:00.
func reviewExpired(local time.Time, monday string) bool {
	d, err := time.Parse(time.DateOnly, monday)
	if err != nil {
		return true
	}
	deadline := time.Date(d.Year(), d.Month(), d.Day()+7, 12, 0, 0, 0, local.Location())
	return !local.Before(deadline)
}

// learningStep — последний шаг вопросов недели: действие финграмотности, если программа идёт.
func (a *App) learningStep(ctx context.Context, monday, before string) (string, [][]Button) {
	w, ok := a.learningWeek(ctx, monday)
	if !ok {
		return before, nil
	}
	return before + "\n\n" + render.LearningQuestion(w.Topic.Title, w.Topic.Action), [][]Button{{
		{Text: "Действие сделано", Data: callbackWeek + monday + ":l:1"},
		{Text: "Перенести", Data: callbackWeek + monday + ":l:0"},
	}}
}

func (a *App) reviewAnchor(ctx context.Context, msgID int, monday string, h habits.Habit) {
	today := a.Now().In(a.location(ctx)).Format(time.DateOnly)
	wake := a.conf().WakeAnchor.String()
	text := render.AnchorFallback(h, wake)
	if table, hs, err := a.promptContext(ctx, today, 7); err == nil {
		if raw, ok := a.ask(ctx, today, llm.AnchorPrompt(table, hs, h.Title), "anchor"); ok {
			if clean, ok := llm.CleanAnswer(raw, llm.MaxAnchorRunes); ok {
				text = escape(clean)
			}
		}
	}
	reply, rows := a.learningStep(ctx, monday, render.AnchorReply(h, wake, text))
	a.edit(ctx, msgID, reply, rows)
}

func (a *App) edit(ctx context.Context, msgID int, text string, rows [][]Button) {
	if err := a.TG.EditRows(ctx, msgID, text, rows); err != nil && !errors.Is(err, ErrMessageGone) {
		a.Log.Warn("callback_edit_failed", "error", err)
	}
}

// ---------- раз в месяц: автоматизм, опросник, выпуск ----------

// monthly — в первое воскресенье месяца: автоматизм привычек и два вопроса о настроении.
func (a *App) monthly(ctx context.Context, local time.Time, f habits.File) {
	month := local.Format("2006-01")
	if local.Day() > 7 {
		return
	}
	sent, err := a.Store.GetKV(ctx, kvMonthlySent)
	if err != nil || sent == month {
		return
	}
	trend, err := a.moodTrend(ctx, local)
	if err != nil {
		a.Log.Warn("monthly_failed", "error", err)
	}
	active := f.Active()
	if _, err := a.TG.SendRows(ctx, render.AutoQuestion(trend), autoRows(month, active, nil)); err != nil {
		a.Log.Warn("monthly_failed", "error", err)
		return
	}
	if _, err := a.TG.SendRows(ctx, render.PHQIntro+"\n\n"+render.PHQQuestions[0], phqRows(month, 1)); err != nil {
		a.Log.Warn("monthly_failed", "error", err)
	}
	if err := a.Store.SetKV(ctx, kvMonthlySent, month); err != nil {
		a.Log.Warn("monthly_failed", "error", err)
	}
	a.Log.Info("monthly_sent", "month", month)
}

// moodTrend — последние четыре недели: настроение рядом с шагами и полом.
func (a *App) moodTrend(ctx context.Context, local time.Time) ([]render.WeekTrend, error) {
	monday := metrics.Monday(local.Format(time.DateOnly))
	var out []render.WeekTrend
	for i := 3; i >= 0; i-- {
		from := metrics.AddDays(monday, -7*i)
		days, err := a.Store.Days(ctx, from, metrics.AddDays(from, 6))
		if err != nil {
			return nil, err
		}
		mood, err := a.Store.Answers(ctx, answerMood, from)
		if err != nil {
			return nil, err
		}
		w := render.WeekTrend{Monday: from, Mood: mood[""]}
		steps, known := 0, 0
		for _, d := range days {
			if d.Steps != nil {
				steps += *d.Steps
				known++
			}
			if d.FloorStatus == metrics.StatusYes {
				w.FloorDays++
			}
		}
		if known > 0 {
			w.Steps = steps / known
		}
		out = append(out, w)
	}
	return out, nil
}

func autoRows(month string, active []habits.Habit, answers map[string]int) [][]Button {
	rows := make([][]Button, 0, len(active))
	for _, h := range active {
		label := h.Emoji
		if label == "" {
			label = string([]rune(h.Title)[:1])
		}
		row := []Button{{Text: label, Data: callbackNoop}}
		for n := 1; n <= 5; n++ {
			text := strconv.Itoa(n)
			if answers[h.ID] == n {
				text += "✓"
			}
			row = append(row, Button{Text: text, Data: fmt.Sprintf("%s%s:a:%s:%d", callbackMonth, month, h.ID, n)})
		}
		rows = append(rows, row)
	}
	return rows
}

func phqRows(month string, q int) [][]Button {
	var rows [][]Button
	for i, label := range render.PHQAnswers {
		b := Button{Text: label, Data: fmt.Sprintf("%s%s:p%d:%d", callbackMonth, month, q, i)}
		if i%2 == 0 {
			rows = append(rows, []Button{b})
		} else {
			rows[len(rows)-1] = append(rows[len(rows)-1], b)
		}
	}
	return rows
}

// onMonthly — ответы на месячные вопросы.
func (a *App) onMonthly(ctx context.Context, rest string, msgID int) string {
	parts := strings.Split(rest, ":")
	if len(parts) < 2 {
		a.Log.Warn("callback_invalid")
		return ""
	}
	month, kind := parts[0], parts[1]
	now := a.Now()
	local := now.In(a.location(ctx))
	f := a.currentHabits(ctx, local.Format(time.DateOnly))
	a.markMu.Lock()
	defer a.markMu.Unlock()

	switch {
	case kind == "a" && len(parts) == 4:
		n, err := strconv.Atoi(parts[3])
		if err != nil || n < 1 || n > 5 {
			return ""
		}
		if err := a.Store.SetAnswer(ctx, answerAuto, month, parts[2], n, now); err != nil {
			a.Log.Error("answer_failed", "error", err)
			return "Не сохранилось, нажми ещё раз."
		}
		answers, err := a.Store.Answers(ctx, answerAuto, month)
		if err != nil {
			return ""
		}
		active := f.Active()
		if len(answers) < len(active) {
			trend, _ := a.moodTrend(ctx, local)
			a.edit(ctx, msgID, render.AutoQuestion(trend), autoRows(month, active, answers))
			return ""
		}
		ready := a.releaseCandidates(ctx, active, answers, local.Format(time.DateOnly))
		var rows [][]Button
		for _, h := range ready {
			rows = append(rows, []Button{{Text: "Выпустить: " + h.Title, Data: callbackMonth + month + ":rel:" + h.ID}})
		}
		if len(rows) > 0 {
			rows = append(rows, []Button{{Text: "Пусть всё остаётся", Data: callbackMonth + month + ":keep"}})
		}
		a.edit(ctx, msgID, render.AutoSummary(active, answers, ready), rows)

	case kind == "rel" && len(parts) == 3:
		h, ok := f.Find(parts[2])
		if !ok || h.Auto {
			return ""
		}
		for i := range f.Habits {
			if f.Habits[i].ID == h.ID {
				f.Habits[i].Auto = true
			}
		}
		if err := habits.Save(a.conf().HabitsFile, f); err != nil {
			a.Log.Error("release_failed", "error", err)
			return "Не получилось, попробуй ещё раз."
		}
		a.Log.Info("habit_released", "habit", h.ID)
		a.edit(ctx, msgID, render.Released(h), nil)
		go a.Tick(context.WithoutCancel(ctx), a.Now()) // карточка и кнопка меню видят новое

	case kind == "keep":
		a.edit(ctx, msgID, "Ок, все привычки остаются на карточке. Через месяц спрошу снова.", nil)

	case (kind == "p1" || kind == "p2") && len(parts) == 3:
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 0 || n > 3 {
			return ""
		}
		if err := a.Store.SetAnswer(ctx, answerPHQ, month, kind[1:], n, now); err != nil {
			a.Log.Error("answer_failed", "error", err)
			return "Не сохранилось, нажми ещё раз."
		}
		if kind == "p1" {
			a.edit(ctx, msgID, render.PHQIntro+"\n\n"+render.PHQQuestions[1], phqRows(month, 2))
			return ""
		}
		answers, err := a.Store.Answers(ctx, answerPHQ, month)
		if err != nil {
			return ""
		}
		a.Log.Info("phq_answered", "month", month)
		a.edit(ctx, msgID, render.PHQResult(answers["1"]+answers["2"]), nil)
	}
	return ""
}

// releaseCandidates — привычки, которые идут сами: 5 сейчас или 4+ два месяца подряд,
// и не моложе releaseMinWeeks недель.
func (a *App) releaseCandidates(ctx context.Context, active []habits.Habit, answers map[string]int, today string) []habits.Habit {
	var out []habits.Habit
	for _, h := range active {
		if h.Week(today) < releaseMinWeeks || answers[h.ID] < 4 {
			continue
		}
		hist, err := a.Store.AnswerHistory(ctx, answerAuto, h.ID, 2)
		if err != nil {
			continue
		}
		if answers[h.ID] == 5 || (len(hist) == 2 && hist[0] >= 4 && hist[1] >= 4) {
			out = append(out, h)
		}
	}
	return out
}

// ---------- данные пропали ----------

// dataGap — три дня подряд нет ни шагов, ни сна: сообщить один раз, пока данные не вернутся.
// Иначе бот тихо превращается в заглушку.
func (a *App) dataGap(ctx context.Context, local time.Time) error {
	today := local.Format(time.DateOnly)
	recent, err := a.Store.Days(ctx, metrics.AddDays(today, -dataGapDays), metrics.AddDays(today, -1))
	if err != nil {
		return err
	}
	gap := true
	for _, d := range recent {
		if d.Steps != nil || d.SleepMinutes != nil {
			gap = false
		}
	}
	alerted, err := a.Store.GetKV(ctx, kvDataGap)
	if err != nil {
		return err
	}
	if !gap {
		if alerted != "" {
			return a.Store.SetKV(ctx, kvDataGap, "")
		}
		return nil
	}
	if alerted != "" {
		return nil
	}
	// Данные раньше были: на свежей установке молчать.
	before, err := a.Store.Days(ctx, metrics.AddDays(today, -30), metrics.AddDays(today, -dataGapDays-1))
	if err != nil {
		return err
	}
	had := false
	for _, d := range before {
		if d.Steps != nil || d.SleepMinutes != nil {
			had = true
		}
	}
	if !had {
		return nil
	}
	if _, err := a.TG.Send(ctx, render.DataGap(a.conf().HealthSource), nil); err != nil {
		return fmt.Errorf("данные пропали: %w", err)
	}
	a.Log.Info("data_gap_alerted")
	return a.Store.SetKV(ctx, kvDataGap, today)
}

// ---------- итог недели ещё одному человеку ----------

// watcher — кто получает итог недели: chat id и имя; 0 — никто.
func (a *App) watcher(ctx context.Context) (int64, string) {
	raw, err := a.Store.GetKV(ctx, kvWatchChat)
	if err != nil || raw == "" {
		return 0, ""
	}
	id, _ := strconv.ParseInt(raw, 10, 64)
	name, _ := a.Store.GetKV(ctx, kvWatchName)
	return id, name
}

func (a *App) shareWeekly(ctx context.Context, data render.WeeklyData) {
	chat, _ := a.watcher(ctx)
	if chat == 0 {
		return
	}
	if err := a.TG.SendTo(ctx, chat, render.WeeklyShare(data, a.rules())); err != nil {
		a.Log.Warn("share_failed", "error", err)
	}
}

// invite заводит ссылку-приглашение и возвращает её.
func (a *App) invite(ctx context.Context) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if err := a.Store.SetKV(ctx, kvWatchToken, token); err != nil {
		return "", err
	}
	return a.TG.StartLink(ctx, "w"+token)
}

// unwatch отключает получателя итога недели.
func (a *App) unwatch(ctx context.Context) error {
	chat, _ := a.watcher(ctx)
	for _, k := range []string{kvWatchChat, kvWatchName, kvWatchToken} {
		if err := a.Store.SetKV(ctx, k, ""); err != nil {
			return err
		}
	}
	if chat != 0 {
		if err := a.TG.SendTo(ctx, chat, "Итог недели сюда больше не приходит."); err != nil {
			a.Log.Warn("share_failed", "error", err)
		}
	}
	return nil
}

// OnWatch — человек открыл ссылку-приглашение. Чужие и устаревшие ссылки молча игнорируются.
func (a *App) OnWatch(ctx context.Context, chatID int64, name, payload string) {
	token, err := a.Store.GetKV(ctx, kvWatchToken)
	if err != nil || token == "" || payload != "w"+token {
		a.Log.Info("watch_rejected")
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Человек по ссылке"
	}
	for k, v := range map[string]string{kvWatchChat: strconv.FormatInt(chatID, 10), kvWatchName: name, kvWatchToken: ""} {
		if err := a.Store.SetKV(ctx, k, v); err != nil {
			a.Log.Error("watch_failed", "error", err)
			return
		}
	}
	a.Log.Info("watch_started")
	if err := a.TG.SendTo(ctx, chatID, "Готово. Раз в неделю, в воскресенье, сюда будет приходить короткий итог недели."); err != nil {
		a.Log.Warn("share_failed", "error", err)
	}
	if _, err := a.TG.Send(ctx, escape(name)+" теперь получает итог недели. Отключить — в Floor, раздел «Сообщения».", nil); err != nil {
		a.Log.Warn("share_failed", "error", err)
	}
	a.Tick(ctx, a.Now()) // кнопка меню покажет получателя
}
