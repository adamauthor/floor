package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"floor/internal/config"
	"floor/internal/habits"
	"floor/internal/health"
	"floor/internal/metrics"
	"floor/internal/render"
	"floor/internal/store"
)

var (
	ctx   = context.Background()
	plus5 = time.FixedZone("+05:00", 5*3600)
	plus7 = time.FixedZone("+07:00", 7*3600)
)

type sent struct {
	id      int
	text    string
	buttons []Button   // все кнопки подряд
	rows    [][]Button // как в сообщении
	chat    int64      // для SendTo
}

type fakeTG struct {
	mu      sync.Mutex
	msgs    []sent
	edits   []sent
	pins    []int
	menus   []sent   // кнопки меню: в text — подпись, в buttons[0].Data — адрес
	removes []string // сообщения, убравшие кнопки под полем ввода
	shared  []sent   // итоги недели второму человеку
	typing  int
	deleted []int
	delErr  error
	sendErr error
	editErr error
	nextID  int
}

func (f *fakeTG) Send(_ context.Context, text string, b []Button) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	f.nextID++
	f.msgs = append(f.msgs, sent{id: f.nextID, text: text, buttons: b, rows: [][]Button{b}})
	return f.nextID, nil
}

func (f *fakeTG) Edit(ctx context.Context, id int, text string) error {
	return f.EditRows(ctx, id, text, nil)
}

func (f *fakeTG) EditRows(_ context.Context, id int, text string, rows [][]Button) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.edits = append(f.edits, sent{id: id, text: text, buttons: flat(rows), rows: rows})
	return nil
}

func (f *fakeTG) SendRows(_ context.Context, text string, rows [][]Button) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	f.nextID++
	f.msgs = append(f.msgs, sent{id: f.nextID, text: text, buttons: flat(rows), rows: rows})
	return f.nextID, nil
}

func (f *fakeTG) SendTo(_ context.Context, chat int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shared = append(f.shared, sent{text: text, chat: chat})
	return nil
}

func (f *fakeTG) StartLink(_ context.Context, payload string) (string, error) {
	return "https://t.me/floor_test_bot?start=" + payload, nil
}

func (f *fakeTG) lastEdit() sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edits) == 0 {
		return sent{}
	}
	return f.edits[len(f.edits)-1]
}

func flat(rows [][]Button) []Button {
	var out []Button
	for _, r := range rows {
		out = append(out, r...)
	}
	return out
}

func (f *fakeTG) Pin(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins = append(f.pins, id)
	return nil
}

func (f *fakeTG) SetMenuApp(_ context.Context, label, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.menus = append(f.menus, sent{text: label, buttons: []Button{{Text: label, Data: url}}})
	return nil
}

func (f *fakeTG) RemoveKeyboard(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.removes = append(f.removes, text)
	return nil
}

func (f *fakeTG) Delete(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		return f.delErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeTG) isDeleted(id int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deleted {
		if d == id {
			return true
		}
	}
	return false
}

func (f *fakeTG) Typing(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing++
	return nil
}

// matching — отправленные сообщения, содержащие подстроку.
func (f *fakeTG) matching(sub string) []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sent
	sub = strings.ReplaceAll(sub, "\u00a0", " ")
	for _, m := range f.msgs {
		if strings.Contains(strings.ReplaceAll(m.text, "\u00a0", " "), sub) {
			out = append(out, m)
		}
	}
	return out
}

type fakeLLM struct {
	mu      sync.Mutex
	reply   string
	err     error
	prompts []string
}

func (f *fakeLLM) Ask(_ context.Context, _, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, prompt)
	return f.reply, f.err
}

type harness struct {
	app *App
	tg  *fakeTG
	llm *fakeLLM
	db  *store.Store
	now time.Time
	dir string
}

func testConfig(dir string) config.Config {
	return config.Config{
		WakeAnchor: config.Clock{Hour: 7}, WakeToleranceMin: 60,
		StepsFloor: 7000, StepsGoal: 10000,
		EveningAt: config.Clock{Hour: 21}, WeeklyAt: config.Clock{Hour: 20},
		DefaultTZ: plus5, LLMMaxCallsPerDay: 30,
		HabitsFile:   filepath.Join(dir, "habits.json"),
		WorkoutTypes: config.SplitList(config.DefaultWorkoutTypes),
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "floor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{tg: &fakeTG{}, llm: &fakeLLM{reply: "Шаг на сегодня: когда встану → зубы."}, db: db, dir: dir}
	h.app = h.build()
	return h
}

func (h *harness) build() *App {
	return &App{
		Cfg: testConfig(h.dir), Store: h.db, TG: h.tg, LLM: h.llm,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return h.now },
	}
}

func at(s string, loc *time.Location) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

func (h *harness) tick(s string, loc *time.Location) {
	h.now = at(s, loc)
	h.app.Tick(ctx, h.now)
}

func exportJSON(generated, offset string, stepsJSON, sleepJSON string) []byte {
	return []byte(fmt.Sprintf(`{"schema":1,"generated_at":%q,"tz_offset":%q,"steps":[%s],"sleep":[%s]}`, generated, offset, stepsJSON, sleepJSON))
}

func (h *harness) apply(t *testing.T, raw []byte) {
	t.Helper()
	exp, err := health.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.Apply(ctx, exp); err != nil {
		t.Fatal(err)
	}
}

const nightSleep = `{"start":"2026-10-30T23:51:00+05:00","end":"2026-10-31T03:10:00+05:00","value":"Сон"},
	{"start":"2026-10-31T03:22:00+05:00","end":"2026-10-31T07:24:00+05:00","value":"Основной"}`

func (h *harness) applyMorningData(t *testing.T) {
	t.Helper()
	h.now = at("2026-10-31 08:00", plus5)
	h.apply(t, exportJSON("2026-10-31T08:00:00+05:00", "+05:00",
		`{"date":"2026-10-30","count":7812},{"date":"2026-10-31","count":1203}`, nightSleep))
}

// --- Выгрузки -------------------------------------------------------------

func TestApplyStoresMetrics(t *testing.T) {
	h := newHarness(t)
	h.applyMorningData(t)
	d, _ := h.db.GetDay(ctx, "2026-10-31")
	if d.WakeAt == nil || d.WakeAt.Format("15:04") != "07:24" || *d.WakeDeltaMin != 24 || *d.SleepMinutes != 441 {
		t.Errorf("день: %+v", d)
	}
	prev, _ := h.db.GetDay(ctx, "2026-10-30")
	if prev.Steps == nil || *prev.Steps != 7812 {
		t.Errorf("вчера: %+v", prev)
	}
}

func TestApplyIdempotent(t *testing.T) {
	h := newHarness(t)
	h.applyMorningData(t)
	before, _ := h.db.GetDay(ctx, "2026-10-31")
	h.applyMorningData(t)
	after, _ := h.db.GetDay(ctx, "2026-10-31")
	if *before.SleepMinutes != *after.SleepMinutes || *before.Steps != *after.Steps {
		t.Errorf("повтор изменил данные: %+v → %+v", before, after)
	}
	raw, _ := h.db.ExportsSince(ctx, h.now.Add(-time.Hour))
	if len(raw) != 1 {
		t.Errorf("выгрузок в базе: %d", len(raw))
	}
}

func TestApplyStepsTakeMaximum(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-10-31 20:00", plus5)
	h.apply(t, exportJSON("2026-10-31T20:00:00+05:00", "+05:00", `{"date":"2026-10-31","count":9000}`, ""))
	h.now = at("2026-10-31 20:01", plus5)
	h.apply(t, exportJSON("2026-10-31T20:01:00+05:00", "+05:00", `{"date":"2026-10-31","count":3000}`, ""))
	d, _ := h.db.GetDay(ctx, "2026-10-31")
	if *d.Steps != 9000 {
		t.Errorf("шаги = %d", *d.Steps)
	}
}

func TestLaterPartialExportDoesNotShrinkNight(t *testing.T) {
	h := newHarness(t)
	h.applyMorningData(t)
	// Выгрузка видит только конец ночи: окно «2 дня» уже срезало начало.
	h.now = at("2026-10-31 09:00", plus5)
	h.apply(t, exportJSON("2026-10-31T09:00:00+05:00", "+05:00", "",
		`{"start":"2026-10-31T03:22:00+05:00","end":"2026-10-31T07:24:00+05:00","value":"Основной"}`))
	d, _ := h.db.GetDay(ctx, "2026-10-31")
	if *d.SleepMinutes != 441 {
		t.Errorf("сон ужался до %d мин", *d.SleepMinutes)
	}
}

func googleExport(generated, from, to, sleepJSON string) []byte {
	return []byte(fmt.Sprintf(`{"schema":1,"source":"google","range_from":%q,"range_to":%q,"generated_at":%q,"tz_offset":"+05:00","steps":[],"sleep":[%s]}`,
		from, to, generated, sleepJSON))
}

func TestCompleteExportReplacesSleep(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-10-31 08:00", plus5)
	h.apply(t, googleExport("2026-10-31T08:00:00+05:00", "2026-10-30", "2026-10-31", nightSleep))
	d, _ := h.db.GetDay(ctx, "2026-10-31")
	if d.SleepMinutes == nil || *d.SleepMinutes != 441 {
		t.Fatalf("день: %+v", d)
	}

	// Fitbit пересчитал ночь: середина оказалась бодрствованием. Старые сегменты
	// не склеиваются с новыми, иначе сон раздулся бы обратно до 441 минуты.
	h.now = at("2026-10-31 09:00", plus5)
	h.apply(t, googleExport("2026-10-31T09:00:00+05:00", "2026-10-30", "2026-10-31",
		`{"start":"2026-10-30T23:51:00+05:00","end":"2026-10-31T03:10:00+05:00","value":"asleep"},
		{"start":"2026-10-31T03:10:00+05:00","end":"2026-10-31T05:00:00+05:00","value":"awake"},
		{"start":"2026-10-31T05:00:00+05:00","end":"2026-10-31T07:24:00+05:00","value":"asleep"}`))
	d, _ = h.db.GetDay(ctx, "2026-10-31")
	if d.SleepMinutes == nil || *d.SleepMinutes != 199 {
		t.Errorf("после пересчёта сон = %v, ожидалось 199 (самый длинный эпизод)", d.SleepMinutes)
	}

	// Сон удалён в приложении: день периода без сна очищается.
	h.now = at("2026-10-31 10:00", plus5)
	h.apply(t, googleExport("2026-10-31T10:00:00+05:00", "2026-10-30", "2026-10-31", ""))
	d, _ = h.db.GetDay(ctx, "2026-10-31")
	if d.SleepMinutes != nil || d.WakeAt != nil || d.WakeDeltaMin != nil {
		t.Errorf("сон не очищен: %+v", d)
	}
}

func TestOlderExportDoesNotMoveTimezone(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-10-31 09:00", plus5)
	h.apply(t, exportJSON("2026-10-31T09:00:00+07:00", "+07:00", "", ""))
	h.apply(t, exportJSON("2026-10-30T09:00:00+05:00", "+05:00", "", ""))
	if tz, _ := h.db.GetKV(ctx, kvTZOffset); tz != "+07:00" {
		t.Errorf("пояс = %s", tz)
	}
}

// --- Утро -----------------------------------------------------------------

func TestMorningOnceWithCorrectNumbers(t *testing.T) {
	h := newHarness(t)
	h.applyMorningData(t)
	h.tick("2026-10-31 07:50", plus5)
	if got := h.tg.matching("☀️"); len(got) != 0 {
		t.Fatalf("рано: %+v", got)
	}
	h.tick("2026-10-31 08:00", plus5)
	h.tick("2026-10-31 08:01", plus5)
	h.tick("2026-10-31 12:00", plus5)
	got := h.tg.matching("☀️")
	if len(got) != 1 {
		t.Fatalf("утренних сообщений: %d", len(got))
	}
	for _, want := range []string{
		"☀️ Суббота, 31 октября", "Подъём 07:24 — в пределах часа от якоря ✅", "Сон 7 ч 21 мин",
		"Шаги вчера 7 812 — пол взят ✅ (цель 10 000)", "Шаг на сегодня: когда встану → зубы.",
	} {
		if !strings.Contains(got[0].text, want) {
			t.Errorf("нет %q в:\n%s", want, got[0].text)
		}
	}
}

func TestMorningWithoutDataComesAtFallback(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 09:59", plus5)
	if len(h.tg.matching("☀️")) != 0 {
		t.Fatal("рано")
	}
	h.tick("2026-10-31 10:00", plus5)
	got := h.tg.matching("☀️")
	if len(got) != 1 || !strings.Contains(got[0].text, render.NoDataText) {
		t.Fatalf("утро: %+v", got)
	}
}

func TestMorningWindowClosedAfterWake(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 14:00", plus5)
	if len(h.tg.matching("☀️")) != 0 {
		t.Fatal("после 14:00 утро не досылается")
	}
}

// --- Вечер ----------------------------------------------------------------

func TestEveningMarksPerHabit(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 21:00", plus5)
	got := h.tg.matching("🌙")
	if len(got) != 1 || len(got[0].rows) != 5 {
		t.Fatalf("вечер: %+v", got)
	}
	if !strings.Contains(got[0].text, "🦷 Зубы\nпол: зубы · цель: зубы до телефона") {
		t.Errorf("текст: %q", got[0].text)
	}
	msg := got[0].id
	tap := func(data string) {
		t.Helper()
		if ans := h.app.OnCallback(ctx, data, msg); ans != "" {
			t.Errorf("%s → %q", data, ans)
		}
	}
	h.now = at("2026-10-31 21:05", plus5)
	teeth := got[0].rows[1][0].Data
	tap(teeth) // пол
	tap(teeth) // цель
	if b := h.tg.lastEdit().rows[1][0].Text; b != "🦷 Зубы · цель ✅" {
		t.Errorf("кнопка после двух нажатий: %q", b)
	}
	tap(teeth)                  // снова без отметки
	tap(teeth)                  // пол
	tap(got[0].rows[4][0].Data) // всё на полу

	marks, _ := h.db.MarksRange(ctx, "2026-10-31", "2026-10-31")
	if m := marks["2026-10-31"]; len(m) != 4 || m["teeth"] != 1 || m["wake"] != 1 {
		t.Errorf("отметки: %v", m)
	}
	if d, _ := h.db.GetDay(ctx, "2026-10-31"); d.FloorStatus != "yes" {
		t.Errorf("статус: %s", d.FloorStatus)
	}
	if e := h.tg.lastEdit(); !strings.HasPrefix(e.text, "Пол дня взят ✅") || len(e.rows) != 0 {
		t.Errorf("итог: %+v", e)
	}
	if len(h.tg.matching("🌙")) != 1 {
		t.Error("новое сообщение вместо редактирования")
	}
}

func TestMissTwiceAndReturnStep(t *testing.T) {
	h := newHarness(t)
	done := func(date string) {
		h.tick(date+" 21:00", plus5)
		got := h.tg.matching("🌙")
		h.now = at(date+" 21:10", plus5)
		last := got[len(got)-1]
		h.app.OnCallback(ctx, last.rows[1][0].Data, last.id) // зубы на полу
		h.app.OnCallback(ctx, last.rows[4][1].Data, last.id) // готово: остальное мимо
	}
	done("2026-10-30")
	if d, _ := h.db.GetDay(ctx, "2026-10-30"); d.FloorStatus != "no" {
		t.Errorf("статус: %s", d.FloorStatus)
	}
	done("2026-10-31")
	if e := h.tg.lastEdit().text; !strings.Contains(e, "🚶 Улица — второй день подряд. Завтра только пол: выйти за дверь.") {
		t.Errorf("итог второго дня: %q", e)
	}
	// Утром шаг — пол первой пропущенной привычки, без Claude.
	calls := len(h.llm.prompts)
	h.tick("2026-11-01 10:00", plus5)
	m := h.tg.matching("☀️")
	if len(m) != 1 || !strings.Contains(m[0].text, "Шаг на сегодня: когда звонит будильник в 07:00 → сел на кровати. Вчера мимо") {
		t.Fatalf("утро: %+v", m)
	}
	if len(h.llm.prompts) != calls {
		t.Error("шаг после пропуска не должен звать Claude")
	}
}

func TestEveningLateWindow(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-11-01 02:00", plus5)
	got := h.tg.matching("🌙")
	if len(got) != 1 || !strings.Contains(got[0].buttons[0].Data, "2026-10-31") {
		t.Fatalf("до 03:00 досылается вчерашний вечер: %+v", got)
	}
	h2 := newHarness(t)
	h2.tick("2026-11-01 03:00", plus5)
	if len(h2.tg.matching("🌙")) != 0 {
		t.Fatal("после 03:00 вечер не досылается")
	}
}

func TestNightMarkAllowedUntilNoon(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-11-01 11:59", plus5)
	if ans := h.app.OnCallback(ctx, "floor:2026-10-31:yes", 5); ans != "" {
		t.Errorf("ответ: %q", ans)
	}
	h.now = at("2026-11-01 12:00", plus5)
	if ans := h.app.OnCallback(ctx, "floor:2026-10-31:no", 5); ans != render.ExpiredReply {
		t.Errorf("ответ: %q", ans)
	}
	d, _ := h.db.GetDay(ctx, "2026-10-31")
	if d.FloorStatus != "yes" {
		t.Errorf("устаревшее нажатие изменило статус: %s", d.FloorStatus)
	}
}

func TestBadCallbacksIgnored(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-10-31 21:00", plus5)
	for _, data := range []string{"", "floor:", "floor:2026-10-31", "floor:2026-10-31:flight", "floor:мусор:yes", "other:2026-10-31:yes"} {
		if ans := h.app.OnCallback(ctx, data, 5); ans != "" {
			t.Errorf("%q → %q", data, ans)
		}
	}
	if len(h.tg.edits) != 0 {
		t.Errorf("правки: %+v", h.tg.edits)
	}
}

func TestTwoMissesInARow(t *testing.T) {
	const line = "Два дня подряд"
	tests := []struct {
		name  string
		setup func(h *harness)
		want  bool
	}{
		{"вчера нет", func(h *harness) {
			h.tick("2026-10-30 21:00", plus5)
			h.now = at("2026-10-30 21:01", plus5)
			h.app.OnCallback(ctx, "floor:2026-10-30:no", 1)
		}, true},
		{"вчера спросили и не отмечено", func(h *harness) { h.tick("2026-10-30 21:00", plus5) }, true},
		{"вчера да", func(h *harness) {
			h.tick("2026-10-30 21:00", plus5)
			h.now = at("2026-10-30 21:01", plus5)
			h.app.OnCallback(ctx, "floor:2026-10-30:yes", 1)
		}, false},
		{"вчера не спрашивали: первый день", func(*harness) {}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.setup(h)
			h.now = at("2026-10-31 21:30", plus5)
			h.app.OnCallback(ctx, "floor:2026-10-31:no", 9)
			last := h.tg.edits[len(h.tg.edits)-1].text
			if strings.Contains(last, line) != tt.want {
				t.Errorf("ответ %q, ожидалась строка про два дня: %v", last, tt.want)
			}
		})
	}
}

// --- Часовой пояс и перезапуск ------------------------------------------

func TestTimezoneChangeMovesScheduleWithoutDoubles(t *testing.T) {
	h := newHarness(t)
	h.applyMorningData(t)
	h.tick("2026-10-31 08:00", plus5)
	if len(h.tg.matching("☀️")) != 1 {
		t.Fatal("нет утра")
	}

	// Перелёт: тот же день, выгрузка уже с +07:00.
	h.now = at("2026-10-31 11:00", plus7)
	h.apply(t, exportJSON("2026-10-31T11:00:00+07:00", "+07:00", `{"date":"2026-10-31","count":2000}`, ""))
	h.tick("2026-10-31 11:01", plus7)
	if len(h.tg.matching("☀️")) != 1 {
		t.Fatal("утро продублировано после смены пояса")
	}

	// Вечер теперь по новому местному времени: 20:59 +07 = рано, 21:00 +07 = пора.
	h.tick("2026-10-31 20:59", plus7)
	if len(h.tg.matching("🌙")) != 0 {
		t.Fatal("вечер раньше времени")
	}
	h.tick("2026-10-31 21:00", plus7)
	h.tick("2026-10-31 21:01", plus7)
	if len(h.tg.matching("🌙")) != 1 {
		t.Fatalf("вечерних: %d", len(h.tg.matching("🌙")))
	}
}

func TestRestartDoesNotResend(t *testing.T) {
	h := newHarness(t)
	h.applyMorningData(t)
	h.tick("2026-10-31 21:00", plus5) // утро (окно закрыто) не уйдёт, вечер уйдёт
	h.tick("2026-11-01 08:00", plus5)
	h.tick("2026-11-01 20:00", plus5)
	before := len(h.tg.msgs)
	cards := len(h.tg.pins)

	h.app = h.build() // новый процесс с той же базой
	h.tick("2026-11-01 20:01", plus5)
	if len(h.tg.msgs) != before {
		t.Errorf("после перезапуска отправлено лишнее: %+v", h.tg.msgs[before:])
	}
	if len(h.tg.pins) != cards {
		t.Error("карточка привычек закреплена повторно")
	}
}

// --- Неделя ---------------------------------------------------------------

func TestWeeklyOnSundayMatchesDB(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-11-01 19:00", plus5)
	for i, n := range []int{4100, 5600, 2300, 5400, 7812, 3000} {
		date := metrics.AddDays("2026-10-26", i)
		if err := h.db.UpsertSteps(ctx, date, "+05:00", n, h.now); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.db.SetFloor(ctx, "2026-10-28", "+05:00", "no", h.now); err != nil {
		t.Fatal(err)
	}
	h.llm.reply = "Получилось: a\nУзкое место: b\nНа следующую неделю: c"

	h.tick("2026-11-01 19:59", plus5)
	if len(h.tg.matching("📅")) != 0 {
		t.Fatal("рано")
	}
	h.tick("2026-11-01 20:00", plus5)
	h.tick("2026-11-01 20:01", plus5)
	got := h.tg.matching("📅")
	if len(got) != 1 {
		t.Fatalf("недельных: %d", len(got))
	}
	for _, want := range []string{
		"📅 Неделя 26 окт — 1 ноя",
		"Шаги    4к  6к  2к  5к  8к  3к  …",
		"Пол     ·   ·   ❌  ·   ·   ·   …",
		"Шаги: пол взят 1 из 6",
		"Якоря на неделю:",
		"Получилось: a\nУзкое место: b\nНа следующую неделю: c",
	} {
		if !strings.Contains(got[0].text, want) {
			t.Errorf("нет %q в:\n%s", want, got[0].text)
		}
	}
	// В воскресенье вечерняя кнопка приходит как обычно.
	h.tick("2026-11-01 21:00", plus5)
	if len(h.tg.matching("🌙")) != 1 {
		t.Error("нет вечерней кнопки в воскресенье")
	}
}

func TestWeeklyNotOnOtherDays(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 20:00", plus5)
	if len(h.tg.matching("📅")) != 0 {
		t.Fatal("сводка не в воскресенье")
	}
}

// --- Claude выключен -----------------------------------------------------

func TestAllScenariosWorkWithoutClaude(t *testing.T) {
	h := newHarness(t)
	h.llm.err = errors.New("claude недоступен")
	h.applyMorningData(t)

	h.tick("2026-11-01 10:00", plus5)
	morning := h.tg.matching("☀️")
	if len(morning) != 1 || !strings.Contains(morning[0].text, "Шаг на сегодня: когда") {
		t.Fatalf("утро: %+v", morning)
	}
	h.tick("2026-11-01 20:00", plus5)
	weekly := h.tg.matching("📅")
	if len(weekly) != 1 || !strings.Contains(weekly[0].text, render.WeeklyFallback) {
		t.Fatalf("неделя: %+v", weekly)
	}
	h.tick("2026-11-01 21:00", plus5)
	if len(h.tg.matching("🌙")) != 1 {
		t.Fatal("нет вечера")
	}
	h.app.OnText(ctx, "как дела")
	if len(h.tg.matching(render.FreeTextFallback)) != 1 {
		t.Fatal("нет ответа на свободный текст")
	}
}

func TestUnusableClaudeAnswersFallBack(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = "просто текст без нужного формата"
	h.tick("2026-11-01 20:00", plus5)
	if len(h.tg.matching(render.WeeklyFallback)) != 1 {
		t.Fatal("плохой формат недели должен уводить на fallback")
	}
}

func TestLLMDailyLimit(t *testing.T) {
	h := newHarness(t)
	h.app.Cfg.LLMMaxCallsPerDay = 2
	h.now = at("2026-10-31 15:00", plus5)
	for range 3 {
		h.app.OnText(ctx, "вопрос")
	}
	if len(h.llm.prompts) != 2 {
		t.Errorf("вызовов Claude: %d", len(h.llm.prompts))
	}
	if len(h.tg.matching(render.FreeTextFallback)) != 1 {
		t.Error("сверх лимита нужен fallback")
	}
}

func TestMorningRetryDoesNotCallClaudeAgain(t *testing.T) {
	h := newHarness(t)
	h.tg.sendErr = errors.New("сеть")
	h.tick("2026-10-31 10:00", plus5)
	calls := len(h.llm.prompts)
	h.tg.sendErr = nil
	h.now = at("2026-10-31 10:10", plus5)
	h.app.backoff.Reset()
	h.tick("2026-10-31 10:10", plus5)
	if len(h.tg.matching("☀️")) != 1 {
		t.Fatal("утро не дошло после восстановления")
	}
	if len(h.llm.prompts) != calls {
		t.Errorf("Claude вызван повторно: %d → %d", calls, len(h.llm.prompts))
	}
}

// --- Telegram недоступен --------------------------------------------------

func TestTelegramDownBackoffAndWindows(t *testing.T) {
	h := newHarness(t)
	h.tg.sendErr = errors.New("сеть")
	h.tick("2026-10-31 13:50", plus5)
	if h.app.backoff.Ready(at("2026-10-31 13:50", plus5)) {
		t.Fatal("после сбоя должен включиться бэкофф")
	}
	// Сеть вернулась, но окно утра закрылось: не досылаем.
	h.tg.sendErr = nil
	h.tick("2026-10-31 14:05", plus5)
	if len(h.tg.matching("☀️")) != 0 {
		t.Fatal("утро дослано после закрытия окна")
	}
	if !h.app.backoff.Ready(at("2026-10-31 14:05", plus5)) {
		t.Fatal("успешный тик должен сбросить бэкофф")
	}
}

// --- Привычки и якоря ----------------------------------------------------

func TestHabitCardPinnedOnceAndUpdated(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 15:00", plus5)
	cards := h.tg.matching("Привычки и якоря")
	if len(cards) != 1 || len(h.tg.pins) != 1 || h.tg.pins[0] != cards[0].id {
		t.Fatalf("карточки %+v, закрепления %v", cards, h.tg.pins)
	}
	if !strings.Contains(cards[0].text, "когда закрыл ноутбук после работы → куртка и 20 минут на улице") {
		t.Errorf("карточка:\n%s", cards[0].text)
	}

	// Ничего не изменилось — запросов нет.
	h.tick("2026-10-31 15:01", plus5)
	if len(h.tg.matching("Привычки и якоря")) != 1 || len(h.tg.edits) != 0 {
		t.Fatal("лишние запросы")
	}

	// Новая неделя с начала привычки — карточка редактируется, а не шлётся заново.
	h.tick("2026-11-07 15:00", plus5)
	if len(h.tg.edits) != 1 || !strings.Contains(h.tg.edits[0].text, "неделя 2 из ~10") {
		t.Fatalf("правки: %+v", h.tg.edits)
	}
	if h.tg.edits[0].id != cards[0].id || len(h.tg.pins) != 1 {
		t.Error("должно редактироваться то же закреплённое сообщение")
	}

	// Правка файла тоже доезжает до карточки.
	writeHabits(t, h, func(f *habits.File) { f.Habits[2].Anchor = "выключил рабочий компьютер" })
	h.tick("2026-11-07 15:01", plus5)
	if len(h.tg.edits) != 2 || !strings.Contains(h.tg.edits[1].text, "выключил рабочий компьютер") {
		t.Fatalf("правки: %+v", h.tg.edits)
	}
}

func writeHabits(t *testing.T, h *harness, change func(*habits.File)) {
	t.Helper()
	raw, err := os.ReadFile(h.app.Cfg.HabitsFile)
	if err != nil {
		t.Fatal(err)
	}
	f, err := habits.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	change(&f)
	out, err := habits.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.app.Cfg.HabitsFile, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHabitCardRepinnedWhenDeleted(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 15:00", plus5)
	h.tg.editErr = ErrMessageGone
	h.tick("2026-11-07 15:00", plus5)
	if len(h.tg.pins) != 2 || len(h.tg.matching("Привычки и якоря")) != 2 {
		t.Fatalf("закрепления %v", h.tg.pins)
	}
	h.tg.editErr = nil
	h.tick("2026-11-07 15:01", plus5)
	if len(h.tg.edits) != 0 || len(h.tg.pins) != 2 {
		t.Fatal("после перезакрепления лишние запросы")
	}
}

func TestBrokenHabitsFileKeepsLastAndWarnsOnce(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 15:00", plus5)
	if err := os.WriteFile(h.app.Cfg.HabitsFile, []byte(`{"habits":[{"id":"a"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.tick("2026-10-31 15:01", plus5)
	h.tick("2026-10-31 15:02", plus5)
	if got := h.tg.matching("habits.json не применён"); len(got) != 1 {
		t.Fatalf("предупреждений: %d", len(got))
	}
	if len(h.tg.edits) != 0 {
		t.Fatalf("карточка не должна меняться: %+v", h.tg.edits)
	}
	h.tick("2026-10-31 21:00", plus5)
	if got := h.tg.matching("🌙"); len(got) != 1 || !strings.Contains(got[0].text, "зубы") {
		t.Fatalf("вечер должен идти по прошлой версии: %+v", got)
	}
}

func TestEveningFollowsHabitsFile(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 15:00", plus5)
	writeHabits(t, h, func(f *habits.File) { f.Habits[3].Paused = true })
	h.tick("2026-10-31 21:00", plus5)
	got := h.tg.matching("🌙")
	if len(got) != 1 || !strings.Contains(got[0].text, "Улица") || strings.Contains(got[0].text, "Вещь на место") || len(got[0].rows) != 4 {
		t.Fatalf("вечер: %+v", got)
	}
}

func TestPromptsCarryHabitsAndAnchors(t *testing.T) {
	h := newHarness(t)
	h.tick("2026-10-31 10:00", plus5)
	if len(h.llm.prompts) != 1 || !strings.Contains(h.llm.prompts[0], "когда закрыл ноутбук после работы → куртка") {
		t.Fatalf("промпт утра: %v", h.llm.prompts)
	}
	h.llm.reply = "Твои якоря в закреплённом."
	h.app.OnText(ctx, "какие у меня якоря?")
	last := h.llm.prompts[len(h.llm.prompts)-1]
	if !strings.Contains(last, "когда вернулся с улицы → кладу одну вещь на место") || !strings.Contains(last, "Адам: какие у меня якоря?") {
		t.Errorf("промпт текста:\n%s", last)
	}
}

// --- Свободный текст ------------------------------------------------------

func TestFreeTextContextAndHistory(t *testing.T) {
	h := newHarness(t)
	h.applyMorningData(t)
	h.llm.reply = "Ответ <b>" + strings.Repeat("длинно. ", 200)
	h.app.OnText(ctx, "первый вопрос")
	h.llm.reply = "Второй ответ."
	h.app.OnText(ctx, "второй вопрос")
	prompt := h.llm.prompts[1]
	for _, want := range []string{"2026-10-31 | 07:24 | +24", "Адам: первый вопрос", "Floor: Ответ", "Адам: второй вопрос"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("нет %q в промпте:\n%s", want, prompt)
		}
	}
	first := h.tg.msgs[0].text
	if strings.Contains(first, "<b>") || !strings.Contains(first, "&lt;b&gt;") {
		t.Errorf("нет экранирования: %s", first[:40])
	}
	if h.tg.typing == 0 {
		t.Error("нет индикатора набора")
	}
	msgs, _ := h.db.RecentMessages(ctx, 10)
	if len(msgs) != 4 {
		t.Errorf("реплик в базе: %d", len(msgs))
	}
}

func TestNonTextReply(t *testing.T) {
	h := newHarness(t)
	h.app.OnNonText(ctx)
	if len(h.tg.matching(render.NonTextReply)) != 1 {
		t.Fatal("нет ответа на голосовое")
	}
	if len(h.llm.prompts) != 0 {
		t.Error("голосовое не должно идти в Claude")
	}
}

func TestOnStartSendsCardImmediately(t *testing.T) {
	h := newHarness(t)
	h.tg.sendErr = errors.New("Forbidden: bot can't initiate conversation")
	h.tick("2026-10-31 15:00", plus5)
	h.tg.sendErr = nil
	h.now = at("2026-10-31 15:00", plus5).Add(time.Second)
	h.app.OnStart(ctx)
	if len(h.tg.matching("Привычки и якоря")) != 1 {
		t.Fatal("после /start карточка должна уйти сразу, несмотря на бэкофф")
	}
}

func TestCleanupRunsDaily(t *testing.T) {
	h := newHarness(t)
	h.now = at("2026-10-31 15:00", plus5)
	if _, err := h.db.InsertExport(ctx, "old", []byte("{}"), h.now.AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	h.tick("2026-10-31 15:00", plus5)
	old, _ := h.db.ExportsSince(ctx, h.now.AddDate(0, 0, -50))
	if len(old) != 0 {
		t.Errorf("старые выгрузки не удалены: %d", len(old))
	}
}

func TestDebugDay(t *testing.T) {
	h := newHarness(t)
	h.applyMorningData(t)
	got, err := h.app.DebugDay(ctx, "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"подъём       07:24 +05:00 (+24 мин, в норме: true)", "сон          7 ч 21 мин", "шаги         1203 (пол 7000, цель 10000)", "пол дня      none"} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в:\n%s", want, got)
		}
	}
	got, _ = h.app.DebugDay(ctx, "2020-01-01")
	if !strings.Contains(got, "нет записи") || !strings.Contains(got, "подъём       нет данных") {
		t.Errorf("пустой день:\n%s", got)
	}
}
