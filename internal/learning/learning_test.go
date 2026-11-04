package learning

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrent(t *testing.T) {
	topics := Default()
	if _, ok := Current(topics, "2026-11-09", "2026-11-08", nil); ok {
		t.Error("до начала")
	}
	w, ok := Current(topics, "2026-11-09", "2026-11-09", nil)
	if !ok || w.Index != 0 || w.Total != 13 || w.Topic.Title != "Куда уходят деньги" {
		t.Errorf("неделя 1: %+v", w)
	}
	if w, _ := Current(topics, "2026-11-09", "2026-11-22", Answers{"2026-11-09": true}); w.Index != 1 {
		t.Errorf("воскресенье второй недели: %+v", w)
	}
	// Перенос: тема повторяется, программа сдвигается.
	a := Answers{"2026-11-09": false}
	if w, _ := Current(topics, "2026-11-09", "2026-11-16", a); w.Index != 0 || !w.Repeat {
		t.Errorf("после переноса: %+v", w)
	}
	// Два переноса подряд — ещё повтор, третий закрывает тему.
	a["2026-11-16"] = false
	if w, _ := Current(topics, "2026-11-09", "2026-11-23", a); w.Index != 0 || w.Postpones != 2 {
		t.Errorf("после двух переносов: %+v", w)
	}
	a["2026-11-23"] = false
	if w, _ := Current(topics, "2026-11-09", "2026-11-30", a); w.Index != 1 || w.Repeat {
		t.Errorf("третий перенос закрывает тему: %+v", w)
	}
	// Без ответа неделя проходит.
	if w, _ := Current(topics, "2026-11-09", "2027-02-01", nil); w.Index != 12 {
		t.Errorf("тринадцатая неделя: %+v", w)
	}
	if _, ok := Current(topics, "2026-11-09", "2027-02-08", nil); ok {
		t.Error("после программы модуль молчит")
	}
	if _, ok := Current(topics, "потом", "2026-11-09", nil); ok {
		t.Error("плохой старт")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if ts, err := Load(filepath.Join(dir, "нет.json")); err != nil || len(ts) != 13 {
		t.Fatalf("без файла: %v", err)
	}
	path := filepath.Join(dir, "l.json")
	_ = os.WriteFile(path, []byte(`{"topics":[{"title":"А","action":"Б"}]}`), 0o600)
	if ts, err := Load(path); err != nil || len(ts) != 1 || ts[0].Title != "А" {
		t.Errorf("свой файл: %v %+v", err, ts)
	}
	for _, bad := range []string{`{`, `{"topics":[]}`, `{"topics":[{"title":"А"}]}`, `{"topcs":[]}`} {
		_ = os.WriteFile(path, []byte(bad), 0o600)
		if ts, err := Load(path); err == nil || len(ts) != 13 {
			t.Errorf("%s: ошибка не замечена", bad)
		}
	}
}
