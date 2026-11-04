// Package core связывает данные, правила и каналы: применяет выгрузки, отправляет
// сообщения по расписанию, обрабатывает кнопки и текст (docs/SPEC.md, разделы 2 и 9).
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"floor/internal/config"
	"floor/internal/habits"
	"floor/internal/health"
	"floor/internal/llm"
	"floor/internal/metrics"
	"floor/internal/render"
	"floor/internal/schedule"
	"floor/internal/store"
)

// ErrMessageGone — редактируемого сообщения больше нет.
var ErrMessageGone = errors.New("сообщение не найдено")

// Button — кнопка под сообщением.
type Button struct {
	Text string
	Data string
}

// Messenger — исходящие действия в Telegram.
type Messenger interface {
	Send(ctx context.Context, text string, buttons []Button) (int, error)
	Edit(ctx context.Context, msgID int, text string) error
	Pin(ctx context.Context, msgID int) error
	Typing(ctx context.Context) error
}

const (
	callbackPrefix = "floor:"

	kvTZOffset      = "tz_offset"
	kvTZAt          = "tz_generated_at"
	kvWeeklySent    = "weekly_sent_for"
	kvCleanupDate   = "cleanup_date"
	kvPinnedMsg     = "habits_pinned_msg"
	kvPinnedHash    = "habits_pinned_hash"
	kvHabitsBad     = "habits_bad_hash"
	exportsLookback = 5 * 24 * time.Hour // столько выгрузок объединяется при пересчёте сна
	callTimeout     = 30 * time.Second
)

// App — состояние приложения.
type App struct {
	Cfg   config.Config
	Store *store.Store
	LLM   llm.Client
	TG    Messenger
	Log   *slog.Logger
	Now   func() time.Time

	tickMu   sync.Mutex // один тик за раз: /start и расписание не должны отправить дважды
	mu       sync.Mutex
	backoff  schedule.Backoff
	habits   habits.File
	stepDate string
	stepText string
}

func (a *App) rules() render.Rules {
	return render.Rules{WakeTolerance: a.Cfg.WakeToleranceMin, StepsFloor: a.Cfg.StepsFloor, StepsGoal: a.Cfg.StepsGoal}
}

func (a *App) location(ctx context.Context) *time.Location {
	offset, err := a.Store.GetKV(ctx, kvTZOffset)
	if err != nil {
		a.Log.Warn("tz_read_failed", "error", err)
	}
	return schedule.Location(offset, a.Cfg.DefaultTZ)
}

// Apply применяет выгрузку. Идемпотентна: повтор той же выгрузки ничего не меняет.
func (a *App) Apply(ctx context.Context, exp health.Export) error {
	now := a.Now()
	fresh, err := a.Store.InsertExport(ctx, exp.SHA256(), exp.Raw, now)
	if err != nil {
		return err
	}
	if !fresh {
		a.Log.Debug("export_duplicate")
		return nil
	}
	if err := a.updateTZ(ctx, exp); err != nil {
		return err
	}
	for _, s := range exp.Steps {
		if err := a.Store.UpsertSteps(ctx, s.Date, exp.TZOffset, s.Count, now); err != nil {
			return err
		}
	}
	nights, err := a.recomputeSleep(ctx, exp, now)
	if err != nil {
		return err
	}
	a.Log.Info("export_applied", "bytes", len(exp.Raw), "steps_days", len(exp.Steps),
		"sleep_samples", len(exp.Sleep), "nights", nights)
	return nil
}

// updateTZ двигает текущий пояс, только если выгрузка новее уже виденной:
// iCloud может доставить старый файл позже нового.
func (a *App) updateTZ(ctx context.Context, exp health.Export) error {
	last, err := a.Store.GetKV(ctx, kvTZAt)
	if err != nil {
		return err
	}
	if last != "" {
		if t, err := time.Parse(time.RFC3339, last); err == nil && !exp.GeneratedAt.After(t) {
			return nil
		}
	}
	if err := a.Store.SetKV(ctx, kvTZOffset, exp.TZOffset); err != nil {
		return err
	}
	return a.Store.SetKV(ctx, kvTZAt, exp.GeneratedAt.Format(time.RFC3339))
}

func segments(samples []health.SleepSample) []metrics.Segment {
	out := make([]metrics.Segment, 0, len(samples))
	for _, s := range samples {
		out = append(out, metrics.Segment{Start: s.Start, End: s.End, Value: s.Value})
	}
	return out
}

// recomputeSleep пересчитывает ночи, которых касается выгрузка, по объединению
// недавних выгрузок: каждая из них видит только «последние 2 дня» и может обрезать ночь.
// Полная выгрузка (Google) заменяет данные за свой период целиком.
func (a *App) recomputeSleep(ctx context.Context, exp health.Export, now time.Time) (int, error) {
	if exp.Complete {
		return a.replaceSleep(ctx, exp, now)
	}
	touched := metrics.Nights(segments(exp.Sleep))
	if len(touched) == 0 {
		return 0, nil
	}
	recent, err := a.Store.ExportsSince(ctx, now.Add(-exportsLookback))
	if err != nil {
		return 0, err
	}
	all := segments(exp.Sleep)
	for _, raw := range recent {
		old, err := health.Parse(raw)
		if err != nil {
			continue
		}
		all = append(all, segments(old.Sleep)...)
	}
	merged := metrics.Nights(all)
	for date := range touched {
		n, ok := merged[date]
		if !ok {
			continue
		}
		delta := metrics.WakeDelta(n.WakeAt, date, a.Cfg.WakeAnchor)
		if err := a.Store.SetSleep(ctx, date, exp.TZOffset, n.Minutes, n.WakeAt, delta, now); err != nil {
			return 0, err
		}
	}
	return len(touched), nil
}

// replaceSleep записывает ночи полной выгрузки и очищает дни её периода без сна:
// Fitbit пересчитывает и правит сон, старые сегменты не должны его раздувать.
func (a *App) replaceSleep(ctx context.Context, exp health.Export, now time.Time) (int, error) {
	nights := metrics.Nights(segments(exp.Sleep))
	n := 0
	for date := exp.From; date <= exp.To; date = metrics.AddDays(date, 1) {
		night, ok := nights[date]
		if !ok {
			if err := a.Store.ClearSleep(ctx, date, now); err != nil {
				return n, err
			}
			continue
		}
		delta := metrics.WakeDelta(night.WakeAt, date, a.Cfg.WakeAnchor)
		if err := a.Store.SetSleep(ctx, date, exp.TZOffset, night.Minutes, night.WakeAt, delta, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Tick — один шаг расписания. Ошибки отправки включают бэкофф, остальное продолжает работу.
func (a *App) Tick(ctx context.Context, t time.Time) {
	a.tickMu.Lock()
	defer a.tickMu.Unlock()

	loc := a.location(ctx)
	local := t.In(loc)
	a.cleanup(ctx, local)

	a.mu.Lock()
	ready := a.backoff.Ready(t)
	a.mu.Unlock()
	if !ready {
		return
	}

	err := errors.Join(
		a.syncHabits(ctx, local),
		a.morning(ctx, local),
		a.evening(ctx, local),
		a.weekly(ctx, local),
	)

	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.backoff.Fail(t)
		a.Log.Warn("tick_failed", "error", err)
		return
	}
	a.backoff.Reset()
}

func (a *App) cleanup(ctx context.Context, local time.Time) {
	today := local.Format(time.DateOnly)
	last, err := a.Store.GetKV(ctx, kvCleanupDate)
	if err != nil || last == today {
		return
	}
	if err := a.Store.Cleanup(ctx, local); err != nil {
		a.Log.Warn("cleanup_failed", "error", err)
		return
	}
	if err := a.Store.SetKV(ctx, kvCleanupDate, today); err != nil {
		a.Log.Warn("cleanup_failed", "error", err)
	}
}

// currentHabits читает файл привычек. При ошибке остаётся последняя рабочая версия,
// а если её нет — стартовый набор: бот не должен молчать из-за опечатки в файле.
func (a *App) currentHabits(ctx context.Context, today string) habits.File {
	raw, f, err := habits.Load(a.Cfg.HabitsFile, today)
	if err != nil {
		a.Log.Warn("habits_invalid", "error", err)
		a.notifyBadHabits(ctx, raw, err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.habits = f
		return f
	}
	if len(a.habits.Habits) == 0 {
		a.habits = habits.Defaults(today)
	}
	return a.habits
}

// notifyBadHabits один раз на версию файла говорит, что правка не применилась.
func (a *App) notifyBadHabits(ctx context.Context, raw []byte, parseErr error) {
	hash := shortHash(string(raw))
	seen, err := a.Store.GetKV(ctx, kvHabitsBad)
	if err != nil || seen == hash {
		return
	}
	text := "habits.json не применён, показываю прошлую версию:\n" + escape(parseErr.Error())
	if _, err := a.TG.Send(ctx, text, nil); err != nil {
		a.Log.Warn("habits_notify_failed", "error", err)
		return
	}
	if err := a.Store.SetKV(ctx, kvHabitsBad, hash); err != nil {
		a.Log.Warn("habits_notify_failed", "error", err)
	}
}

func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// syncHabits держит закреплённую карточку привычек актуальной: и после правки файла,
// и когда сменилась неделя. Текст сравнивается по хешу, лишних запросов нет.
func (a *App) syncHabits(ctx context.Context, local time.Time) error {
	today := local.Format(time.DateOnly)
	f := a.currentHabits(ctx, today)
	card := render.HabitCard(f.Active(), f.FloorLabels(), today, a.Cfg.WakeAnchor.String())
	hash := shortHash(card)

	stored, err := a.Store.GetKV(ctx, kvPinnedHash)
	if err != nil {
		return err
	}
	msgRaw, err := a.Store.GetKV(ctx, kvPinnedMsg)
	if err != nil {
		return err
	}
	msgID, _ := strconv.Atoi(msgRaw)
	if msgID != 0 && stored == hash {
		return nil
	}

	if msgID != 0 {
		err := a.TG.Edit(ctx, msgID, card)
		if err == nil {
			a.Log.Info("habits_card_updated")
			return a.Store.SetKV(ctx, kvPinnedHash, hash)
		}
		if !errors.Is(err, ErrMessageGone) {
			return fmt.Errorf("обновление карточки: %w", err)
		}
	}

	id, err := a.TG.Send(ctx, card, nil)
	if err != nil {
		return fmt.Errorf("отправка карточки: %w", err)
	}
	if err := a.TG.Pin(ctx, id); err != nil {
		a.Log.Warn("habits_pin_failed", "error", err)
	}
	if err := a.Store.SetKV(ctx, kvPinnedMsg, strconv.Itoa(id)); err != nil {
		return err
	}
	a.Log.Info("habits_card_pinned")
	return a.Store.SetKV(ctx, kvPinnedHash, hash)
}

func (a *App) morning(ctx context.Context, local time.Time) error {
	date := local.Format(time.DateOnly)
	day, err := a.Store.GetDay(ctx, date)
	if err != nil {
		return err
	}
	if day.MorningSentAt != nil || !schedule.MorningDue(local, a.Cfg.WakeAnchor, day.WakeAt) {
		return nil
	}
	yesterday, err := a.Store.GetDay(ctx, metrics.AddDays(date, -1))
	if err != nil {
		return err
	}
	data := render.MorningData{Date: date, StepsYesterday: yesterday.Steps, Step: a.stepOfDay(ctx, date)}
	if day.WakeAt != nil && day.WakeAt.In(local.Location()).Format(time.DateOnly) == date {
		data.WakeAt, data.WakeDeltaMin, data.SleepMinutes = day.WakeAt, day.WakeDeltaMin, day.SleepMinutes
	}
	if _, err := a.TG.Send(ctx, render.Morning(data, a.rules()), nil); err != nil {
		return fmt.Errorf("утро: %w", err)
	}
	a.Log.Info("morning_sent", "date", date)
	return a.Store.MarkMorningSent(ctx, date, local.Format("-07:00"), local)
}

// stepOfDay запоминает шаг на дату: повтор отправки после сбоя не тратит вызов Claude.
func (a *App) stepOfDay(ctx context.Context, date string) string {
	a.mu.Lock()
	if a.stepDate == date {
		defer a.mu.Unlock()
		return a.stepText
	}
	a.mu.Unlock()

	step := llm.FallbackStep(date)
	table, hs, err := a.promptContext(ctx, date, 7)
	if err == nil {
		if raw, ok := a.ask(ctx, date, llm.MorningPrompt(table, hs), "morning"); ok {
			if clean, ok := llm.CleanStep(raw); ok {
				step = clean
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stepDate, a.stepText = date, step
	return step
}

func (a *App) evening(ctx context.Context, local time.Time) error {
	date, due := schedule.EveningDate(local, a.Cfg.EveningAt)
	if !due {
		return nil
	}
	day, err := a.Store.GetDay(ctx, date)
	if err != nil {
		return err
	}
	if day.EveningSentAt != nil {
		return nil
	}
	f := a.currentHabits(ctx, date)
	buttons := []Button{
		{Text: "✅ Да", Data: callbackPrefix + date + ":" + metrics.StatusYes},
		{Text: "❌ Нет", Data: callbackPrefix + date + ":" + metrics.StatusNo},
	}
	id, err := a.TG.Send(ctx, render.Evening(f.FloorLabels()), buttons)
	if err != nil {
		return fmt.Errorf("вечер: %w", err)
	}
	a.Log.Info("evening_sent", "date", date)
	return a.Store.MarkEveningSent(ctx, date, local.Format("-07:00"), id, local)
}

func (a *App) weekly(ctx context.Context, local time.Time) error {
	key, due := schedule.WeeklyKey(local, a.Cfg.WeeklyAt)
	if !due {
		return nil
	}
	sent, err := a.Store.GetKV(ctx, kvWeeklySent)
	if err != nil {
		return err
	}
	if sent == key {
		return nil
	}
	today := local.Format(time.DateOnly)
	week, err := a.Store.Days(ctx, key, metrics.AddDays(key, 6))
	if err != nil {
		return err
	}
	summary := render.WeeklyFallback
	table, hs, err := a.promptContext(ctx, today, 14)
	if err == nil {
		if raw, ok := a.ask(ctx, today, llm.WeeklyPrompt(table, hs), "weekly"); ok {
			if clean, ok := llm.CleanWeekly(raw); ok {
				summary = clean
			}
		}
	}
	f := a.currentHabits(ctx, today)
	text := render.Weekly(render.WeeklyData{
		Monday: key, Today: today, Days: plainDays(week), Habits: f.Active(),
		Wake: a.Cfg.WakeAnchor.String(), Summary: summary,
	}, a.rules())
	if _, err := a.TG.Send(ctx, text, nil); err != nil {
		return fmt.Errorf("неделя: %w", err)
	}
	a.Log.Info("weekly_sent", "week", key)
	return a.Store.SetKV(ctx, kvWeeklySent, key)
}

func plainDays(days []store.Day) []metrics.Day {
	out := make([]metrics.Day, 0, len(days))
	for _, d := range days {
		out = append(out, d.Day)
	}
	return out
}

// promptContext собирает таблицу последних n дней и список привычек для промпта.
func (a *App) promptContext(ctx context.Context, today string, n int) (table, hs string, err error) {
	days, err := a.Store.Days(ctx, metrics.AddDays(today, 1-n), today)
	if err != nil {
		return "", "", err
	}
	f := a.currentHabits(ctx, today)
	return render.ContextTable(plainDays(days)), render.HabitsContext(f.Active(), today, a.Cfg.WakeAnchor.String()), nil
}

// ask вызывает Claude с учётом дневного лимита; false — использовать fallback.
func (a *App) ask(ctx context.Context, date, prompt, scenario string) (string, bool) {
	allowed, err := a.Store.TakeLLMCall(ctx, date, a.Cfg.LLMMaxCallsPerDay)
	if err != nil || !allowed {
		a.Log.Warn("llm_fallback", "scenario", scenario, "reason", "limit", "error", err)
		return "", false
	}
	text, err := a.LLM.Ask(ctx, llm.System, prompt)
	if err != nil {
		a.Log.Warn("llm_fallback", "scenario", scenario, "reason", "error", "error", err)
		return "", false
	}
	a.Log.Info("llm_ok", "scenario", scenario, "chars", len(text))
	return text, true
}

// OnCallback обрабатывает нажатие кнопки пола и возвращает текст всплывающего ответа.
func (a *App) OnCallback(ctx context.Context, data string, msgID int) string {
	date, status, ok := parseCallback(data)
	if !ok {
		a.Log.Warn("callback_invalid")
		return ""
	}
	loc := a.location(ctx)
	local := a.Now().In(loc)
	if schedule.FloorExpired(local, date) {
		a.Log.Info("callback_expired", "date", date)
		return render.ExpiredReply
	}
	two, err := a.twoInARow(ctx, date, status)
	if err != nil {
		a.Log.Warn("callback_failed", "error", err)
	}
	if err := a.Store.SetFloor(ctx, date, local.Format("-07:00"), status, local); err != nil {
		a.Log.Error("callback_failed", "error", err)
		return "Не сохранилось, нажми ещё раз."
	}
	a.Log.Info("floor_marked", "date", date, "status", status)
	if err := a.TG.Edit(ctx, msgID, render.FloorReply(status, two)); err != nil && !errors.Is(err, ErrMessageGone) {
		a.Log.Warn("callback_edit_failed", "error", err)
	}
	return ""
}

// twoInARow — сегодня «нет», и вчера тоже пропуск. День, в который бот не спрашивал
// (Мак спал, бота ещё не было), пропуском не считается: вопроса не было.
func (a *App) twoInARow(ctx context.Context, date, status string) (bool, error) {
	if status != metrics.StatusNo {
		return false, nil
	}
	prev, err := a.Store.GetDay(ctx, metrics.AddDays(date, -1))
	if err != nil {
		return false, err
	}
	asked := prev.EveningSentAt != nil || prev.FloorStatus == metrics.StatusNo
	return asked && metrics.IsMiss(prev.FloorStatus), nil
}

func parseCallback(data string) (date, status string, ok bool) {
	rest, found := strings.CutPrefix(data, callbackPrefix)
	if !found {
		return "", "", false
	}
	date, status, found = strings.Cut(rest, ":")
	if !found || (status != metrics.StatusYes && status != metrics.StatusNo) {
		return "", "", false
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return "", "", false
	}
	return date, status, true
}

// OnStart — первый контакт: теперь бот может писать, пауза отправки не нужна.
func (a *App) OnStart(ctx context.Context) {
	a.mu.Lock()
	a.backoff.Reset()
	a.mu.Unlock()
	a.Tick(ctx, a.Now())
}

// OnNonText отвечает на голосовые и всё, что не текст.
func (a *App) OnNonText(ctx context.Context) {
	if _, err := a.TG.Send(ctx, render.NonTextReply, nil); err != nil {
		a.Log.Warn("reply_failed", "error", err)
	}
}

// OnText отвечает на свободный текст с контекстом 14 дней и последних 20 реплик.
func (a *App) OnText(ctx context.Context, text string) {
	now := a.Now()
	if err := a.Store.AddMessage(ctx, "user", text, now); err != nil {
		a.Log.Warn("message_save_failed", "error", err)
	}
	stop := a.keepTyping(ctx)
	reply := a.freeReply(ctx, now)
	stop()
	if _, err := a.TG.Send(ctx, escape(reply), nil); err != nil {
		a.Log.Warn("reply_failed", "error", err)
		return
	}
	if reply != render.FreeTextFallback {
		if err := a.Store.AddMessage(ctx, "assistant", reply, a.Now()); err != nil {
			a.Log.Warn("message_save_failed", "error", err)
		}
	}
}

func (a *App) freeReply(ctx context.Context, now time.Time) string {
	today := now.In(a.location(ctx)).Format(time.DateOnly)
	table, hs, err := a.promptContext(ctx, today, 14)
	if err != nil {
		a.Log.Warn("context_failed", "error", err)
		return render.FreeTextFallback
	}
	history, err := a.Store.RecentMessages(ctx, 20)
	if err != nil {
		a.Log.Warn("context_failed", "error", err)
		return render.FreeTextFallback
	}
	turns := make([]llm.Turn, 0, len(history))
	for _, m := range history {
		turns = append(turns, llm.Turn{Role: m.Role, Text: m.Text})
	}
	raw, ok := a.ask(ctx, today, llm.FreePrompt(table, hs, turns), "free_text")
	if !ok {
		return render.FreeTextFallback
	}
	return llm.Truncate(raw, llm.MaxFreeRunes)
}

// keepTyping показывает «печатает…», пока Claude думает: индикатор живёт 5 секунд.
func (a *App) keepTyping(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			_ = a.TG.Typing(ctx)
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// DebugDay печатает метрики дня: проверка M1 без Telegram.
func (a *App) DebugDay(ctx context.Context, date string) (string, error) {
	d, err := a.Store.GetDay(ctx, date)
	if err != nil {
		return "", err
	}
	prev, err := a.Store.GetDay(ctx, metrics.AddDays(date, -1))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "дата         %s\n", date)
	if !d.Exists {
		b.WriteString("в базе нет записи за этот день\n")
	}
	fmt.Fprintf(&b, "якорь        %s (допуск ±%d мин)\n", a.Cfg.WakeAnchor, a.Cfg.WakeToleranceMin)
	if d.WakeAt != nil && d.WakeDeltaMin != nil {
		fmt.Fprintf(&b, "подъём       %s (%+d мин, в норме: %v)\n", d.WakeAt.Format("15:04 -07:00"), *d.WakeDeltaMin,
			metrics.InNorm(*d.WakeDeltaMin, a.Cfg.WakeToleranceMin))
	} else {
		b.WriteString("подъём       нет данных\n")
	}
	if d.SleepMinutes != nil {
		fmt.Fprintf(&b, "сон          %s\n", render.Duration(*d.SleepMinutes))
	} else {
		b.WriteString("сон          нет данных\n")
	}
	if d.Steps != nil {
		fmt.Fprintf(&b, "шаги         %d (пол %d, цель %d)\n", *d.Steps, a.Cfg.StepsFloor, a.Cfg.StepsGoal)
	} else {
		b.WriteString("шаги         нет данных\n")
	}
	fmt.Fprintf(&b, "пол дня      %s (вчера: %s)\n", d.FloorStatus, prev.FloorStatus)
	fmt.Fprintf(&b, "утро         отправлено: %v\n", d.MorningSentAt != nil)
	fmt.Fprintf(&b, "вечер        отправлен: %v\n", d.EveningSentAt != nil)
	return b.String(), nil
}
