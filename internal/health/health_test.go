package health

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestParseExample(t *testing.T) {
	raw, err := os.ReadFile("testdata/export.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.TZOffset != "+05:00" || len(e.Steps) != 2 || e.Steps[1].Count != 1203 {
		t.Errorf("неожиданный разбор: %+v", e)
	}
	wantValues := []string{ValueInBed, ValueCore, ValueAwake, ValueCore}
	for i, s := range e.Sleep {
		if s.Value != wantValues[i] {
			t.Errorf("sleep[%d].value = %s, ожидалось %s", i, s.Value, wantValues[i])
		}
	}
	if e.SHA256() == "" {
		t.Error("пустой sha256")
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name, in string
		schema   bool
	}{
		{"не JSON", "{", false},
		{"другая схема", `{"schema":2,"generated_at":"2026-10-31T08:00:00+05:00"}`, true},
		{"нет схемы", `{"generated_at":"2026-10-31T08:00:00+05:00"}`, true},
		{"плохой generated_at", `{"schema":1,"generated_at":"вчера"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if errors.Is(err, ErrSchema) != tt.schema {
				t.Errorf("ErrSchema = %v, ожидалось %v: %v", errors.Is(err, ErrSchema), tt.schema, err)
			}
		})
	}
}

func TestParseTolerant(t *testing.T) {
	raw := `{"schema":"1.0","generated_at":"2026-10-31T08:00:00+07:00","tz_offset":"непонятно",
	 "steps":[{"date":"2026-10-30","count":4812.7},{"date":"мусор","count":1},{"date":"2026-10-29","count":-5}],
	 "sleep":[{"start":"2026-10-31T01:00:00+07:00","end":"2026-10-31T00:00:00+07:00","value":"Сон"},
	          {"start":"мусор","end":"2026-10-31T00:00:00+07:00","value":"Сон"},
	          {"start":"2026-10-31T01:00:00+07:00","end":"2026-10-31T06:00:00+07:00","value":"Сон"}]}`
	e, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if e.TZOffset != "+07:00" {
		t.Errorf("смещение должно взяться из generated_at, получено %q", e.TZOffset)
	}
	if len(e.Steps) != 1 || e.Steps[0].Count != 4812 {
		t.Errorf("steps = %+v", e.Steps)
	}
	if len(e.Sleep) != 1 || e.Sleep[0].Value != ValueAsleep {
		t.Errorf("sleep = %+v", e.Sleep)
	}
}

func TestParseEmptyIsNotError(t *testing.T) {
	e, err := Parse([]byte(`{"schema":1,"generated_at":"2026-10-31T08:00:00+05:00","tz_offset":"+05:00","steps":[],"sleep":[]}`))
	if err != nil || len(e.Steps) != 0 || len(e.Sleep) != 0 {
		t.Errorf("e = %+v, err = %v", e, err)
	}
}

func TestNormalizeSleepValue(t *testing.T) {
	tests := map[string]string{
		"Сон": ValueAsleep, "Основной": ValueCore, "Глубокий": ValueDeep, "REM": ValueREM,
		"В кровати": ValueInBed, "Бодрствование": ValueAwake, " ASLEEP ": ValueAsleep,
		"unspecified": ValueUnspecified, "что-то новое": ValueUnknown,
	}
	for in, want := range tests {
		if got := NormalizeSleepValue(in); got != want {
			t.Errorf("%q → %q, ожидалось %q", in, got, want)
		}
	}
}

func TestNormalizeOffset(t *testing.T) {
	tests := map[string]string{"+05:00": "+05:00", "+0500": "+05:00", "-03:30": "-03:30", "Z": "+00:00", "": "", "5": ""}
	for in, want := range tests {
		if got := NormalizeOffset(in); got != want {
			t.Errorf("%q → %q, ожидалось %q", in, got, want)
		}
	}
	loc, ok := OffsetLocation("+07:00")
	if !ok {
		t.Fatal("смещение не распознано")
	}
	if _, sec := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone(); sec != 7*3600 {
		t.Errorf("смещение = %d", sec)
	}
	if _, ok := OffsetLocation("мусор"); ok {
		t.Error("мусор распознан как смещение")
	}
}

const validExport = `{"schema":1,"generated_at":"2026-10-31T08:00:00+05:00","tz_offset":"+05:00","steps":[],"sleep":[]}`

func TestFileSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &FileSource{Path: path, Interval: 5 * time.Millisecond, Log: quiet()}
	ch, err := src.Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}

	write := func(body string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)

	write(validExport, base)
	select {
	case e := <-ch:
		if e.TZOffset != "+05:00" {
			t.Errorf("e = %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("нет выгрузки")
	}

	// Битый файл пропускается, следующий валидный доходит.
	write("{", base.Add(time.Minute))
	time.Sleep(30 * time.Millisecond)
	write(strings.Replace(validExport, "+05:00\",\"steps", "+07:00\",\"steps", 1), base.Add(2*time.Minute))
	select {
	case e := <-ch:
		if e.TZOffset != "+07:00" {
			t.Errorf("e = %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("нет второй выгрузки")
	}

	cancel()
	for range ch { // канал должен закрыться
	}
}

func TestHTTPHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan Export, 1)
	src := &HTTPSource{Token: "secret", Log: quiet()}
	srv := httptest.NewServer(src.Handler(ctx, out))
	defer srv.Close()

	post := func(token string, body []byte) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ingest", bytes.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, body := post("", []byte(validExport)); code != http.StatusUnauthorized || body != "" {
		t.Errorf("без токена: %d %q", code, body)
	}
	if code, body := post("wrong", []byte(validExport)); code != http.StatusUnauthorized || body != "" {
		t.Errorf("неверный токен: %d %q", code, body)
	}
	if code, _ := post("secret", []byte("{")); code != http.StatusBadRequest {
		t.Errorf("битое тело: %d", code)
	}
	if code, _ := post("secret", bytes.Repeat([]byte("x"), MaxBodyBytes+1)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("большое тело: %d", code)
	}
	if code, _ := post("secret", []byte(validExport)); code != http.StatusNoContent {
		t.Errorf("валидная выгрузка: %d", code)
	}
	if len(out) != 1 {
		t.Error("выгрузка не дошла до канала")
	}
}

func TestHTTPSourceLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := (&HTTPSource{Addr: "127.0.0.1:0", Token: "t", Log: quiet()}).Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, open := <-ch:
		if open {
			t.Error("канал должен закрыться без данных")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("канал не закрылся")
	}
}
