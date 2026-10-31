package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var (
	ctx = context.Background()
	now = time.Date(2026, 10, 31, 8, 0, 0, 0, time.FixedZone("", 5*3600))
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "sub", "floor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenTwiceKeepsDataAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floor.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.GetKV(ctx, "k"); v != "v" {
		t.Errorf("значение потеряно: %q", v)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("права базы = %v", info.Mode().Perm())
	}
}

func TestMissingDay(t *testing.T) {
	d, err := open(t).GetDay(ctx, "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	if d.Exists || d.FloorStatus != "none" || d.Steps != nil {
		t.Errorf("d = %+v", d)
	}
}

func TestStepsOnlyGrow(t *testing.T) {
	s := open(t)
	for _, n := range []int{1000, 5000, 3000} {
		if err := s.UpsertSteps(ctx, "2026-10-31", "+05:00", n, now); err != nil {
			t.Fatal(err)
		}
	}
	d, _ := s.GetDay(ctx, "2026-10-31")
	if d.Steps == nil || *d.Steps != 5000 {
		t.Errorf("шаги = %v", d.Steps)
	}
}

func TestSleepRoundTripKeepsOffset(t *testing.T) {
	s := open(t)
	wake := time.Date(2026, 10, 31, 7, 24, 0, 0, time.FixedZone("", 7*3600))
	if err := s.SetSleep(ctx, "2026-10-31", "+07:00", 400, wake, 24, now); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetDay(ctx, "2026-10-31")
	if d.WakeAt == nil || d.WakeAt.Format(time.RFC3339) != "2026-10-31T07:24:00+07:00" {
		t.Errorf("подъём = %v", d.WakeAt)
	}
	if *d.SleepMinutes != 400 || *d.WakeDeltaMin != 24 || d.TZOffset != "+07:00" {
		t.Errorf("d = %+v", d)
	}
}

func TestFloorAndSentMarks(t *testing.T) {
	s := open(t)
	const date = "2026-10-31"
	if err := s.MarkEveningSent(ctx, date, "+05:00", 77, now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMorningSent(ctx, date, "+05:00", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFloor(ctx, date, "+05:00", "no", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFloor(ctx, date, "+05:00", "yes", now); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetDay(ctx, date)
	if d.FloorStatus != "yes" || d.EveningMsgID == nil || *d.EveningMsgID != 77 || d.MorningSentAt == nil || d.EveningSentAt == nil {
		t.Errorf("d = %+v", d)
	}
	if err := s.SetFloor(ctx, date, "+05:00", "flight", now); err == nil {
		t.Error("статус вне списка должен отклоняться схемой")
	}
}

func TestDaysFillsGaps(t *testing.T) {
	s := open(t)
	if err := s.UpsertSteps(ctx, "2026-10-28", "+05:00", 100, now); err != nil {
		t.Fatal(err)
	}
	days, err := s.Days(ctx, "2026-10-27", "2026-10-29")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 3 || days[0].Exists || !days[1].Exists || days[2].Date != "2026-10-29" {
		t.Errorf("days = %+v", days)
	}
}

func TestExportsIdempotentAndRetention(t *testing.T) {
	s := open(t)
	ok, err := s.InsertExport(ctx, "abc", []byte("{}"), now)
	if err != nil || !ok {
		t.Fatalf("первая вставка: %v %v", ok, err)
	}
	ok, err = s.InsertExport(ctx, "abc", []byte("{}"), now)
	if err != nil || ok {
		t.Fatalf("повтор: %v %v", ok, err)
	}
	if _, err := s.InsertExport(ctx, "old", []byte("{}"), now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ExportsSince(ctx, now.Add(-40*24*time.Hour))
	if len(all) != 2 {
		t.Fatalf("выгрузок = %d", len(all))
	}
	if err := s.Cleanup(ctx, now); err != nil {
		t.Fatal(err)
	}
	all, _ = s.ExportsSince(ctx, now.Add(-40*24*time.Hour))
	if len(all) != 1 {
		t.Errorf("после очистки выгрузок = %d", len(all))
	}
}

func TestMessagesOrderAndRetention(t *testing.T) {
	s := open(t)
	if err := s.AddMessage(ctx, "user", "старое", now.Add(-61*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"раз", "два", "три"} {
		if err := s.AddMessage(ctx, "user", text, now); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.RecentMessages(ctx, 2)
	if len(got) != 2 || got[0].Text != "два" || got[1].Text != "три" {
		t.Errorf("got = %+v", got)
	}
	if err := s.Cleanup(ctx, now); err != nil {
		t.Fatal(err)
	}
	got, _ = s.RecentMessages(ctx, 10)
	if len(got) != 3 {
		t.Errorf("после очистки = %d", len(got))
	}
}

func TestTakeLLMCallLimitAndConcurrency(t *testing.T) {
	s := open(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.TakeLLMCall(ctx, "2026-10-31", 5)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 5 {
		t.Errorf("разрешено %d вызовов, ожидалось 5", allowed)
	}
	if ok, _ := s.TakeLLMCall(ctx, "2026-11-01", 5); !ok {
		t.Error("новый день должен начинаться с нуля")
	}
	if err := s.Cleanup(ctx, now.AddDate(0, 0, 3)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetKV(ctx, "llm_calls:2026-10-31"); v != "" {
		t.Errorf("старый счётчик не удалён: %q", v)
	}
}

func TestKVMissing(t *testing.T) {
	if v, err := open(t).GetKV(ctx, "нет"); v != "" || err != nil {
		t.Errorf("v = %q, err = %v", v, err)
	}
}
