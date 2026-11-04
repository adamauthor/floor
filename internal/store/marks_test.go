package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMarksAndAnswers(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for _, m := range []struct {
		date, id string
		level    int
	}{{"2026-11-01", "a", 1}, {"2026-11-01", "a", 2}, {"2026-11-01", "b", 0}, {"2026-11-02", "a", 1}, {"2026-11-02", "a", -1}} {
		if err := s.SetMark(ctx, m.date, m.id, m.level, now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.MarksRange(ctx, "2026-11-01", "2026-11-02")
	if err != nil || got["2026-11-01"]["a"] != 2 || got["2026-11-01"]["b"] != 0 || len(got["2026-11-02"]) != 0 {
		t.Fatalf("отметки %v, %v", got, err)
	}
	for _, a := range []struct {
		period string
		v      int
	}{{"2026-10", 3}, {"2026-11", 4}, {"2026-12", 5}, {"2026-12", 2}} {
		if err := s.SetAnswer(ctx, "auto", a.period, "a", a.v, now); err != nil {
			t.Fatal(err)
		}
	}
	hist, err := s.AnswerHistory(ctx, "auto", "a", 2)
	if err != nil || len(hist) != 2 || hist[0] != 4 || hist[1] != 2 {
		t.Errorf("история %v, %v", hist, err)
	}
	if ans, _ := s.Answers(ctx, "auto", "2026-12"); ans["a"] != 2 {
		t.Errorf("ответы %v", ans)
	}
}
