package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

const googleStepsResp = `{"rollupDataPoints":[
 {"civilStartTime":{"date":{"year":2026,"month":11,"day":3}},"steps":{"countSum":"8123"}},
 {"civilStartTime":{"date":{"year":2026,"month":11,"day":4}},"steps":{"countSum":"1500"}},
 {"civilStartTime":{"date":{"year":2026,"month":11,"day":2}}}
]}`

const googleSleepResp = `{"dataPoints":[
 {"sleep":{"interval":{"startTime":"2026-11-03T18:10:00Z","endTime":"2026-11-04T01:40:00Z"},
  "stages":[
   {"startTime":"2026-11-03T18:10:00Z","endTime":"2026-11-03T21:00:00Z","type":"LIGHT"},
   {"startTime":"2026-11-03T21:00:00Z","endTime":"2026-11-03T21:20:00Z","type":"AWAKE"},
   {"startTime":"2026-11-03T21:20:00Z","endTime":"2026-11-03T23:00:00Z","type":"DEEP"},
   {"startTime":"2026-11-03T23:00:00Z","endTime":"2026-11-04T01:40:00Z","type":"REM"}]}},
 {"sleep":{"interval":{"startTime":"2026-11-03T09:00:00Z","endTime":"2026-11-03T09:40:00Z"}}}
]}`

type fakeGoogle struct {
	status  atomic.Int32
	filters atomic.Value
	bodies  atomic.Value
}

func (f *fakeGoogle) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st := f.status.Load(); st != 0 {
			w.WriteHeader(int(st))
			_, _ = io.WriteString(w, `{"error":{"code":401}}`)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v4/users/me/dataTypes/steps/dataPoints:dailyRollUp":
			b, _ := io.ReadAll(r.Body)
			f.bodies.Store(string(b))
			_, _ = io.WriteString(w, googleStepsResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v4/users/me/dataTypes/sleep/dataPoints":
			f.filters.Store(r.URL.Query().Get("filter"))
			_, _ = io.WriteString(w, googleSleepResp)
		default:
			t.Errorf("неожиданный запрос %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newGoogleSource(t *testing.T, f *fakeGoogle) *GoogleSource {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	loc := time.FixedZone("+05:00", 5*3600)
	return &GoogleSource{
		Client:   srv.Client(),
		BaseURL:  srv.URL,
		Loc:      loc,
		Interval: 10 * time.Millisecond,
		Now:      func() time.Time { return time.Date(2026, 11, 4, 9, 0, 0, 0, loc) },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestGoogleFetch(t *testing.T) {
	f := &fakeGoogle{}
	exp, err := newGoogleSource(t, f).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exp.TZOffset != "+05:00" {
		t.Errorf("tz = %q", exp.TZOffset)
	}
	want := []StepsDay{{"2026-11-03", 8123}, {"2026-11-04", 1500}}
	if len(exp.Steps) != 2 || exp.Steps[0] != want[0] || exp.Steps[1] != want[1] {
		t.Errorf("шаги = %+v, нужно %+v (день без суммы пропускается)", exp.Steps, want)
	}
	values := []string{}
	for _, s := range exp.Sleep {
		values = append(values, s.Value)
	}
	if got := strings.Join(values, ","); got != "core,awake,deep,rem,asleep" {
		t.Errorf("стадии = %s", got)
	}

	var body struct {
		Range struct {
			Start, End map[string]civilDate
		}
		WindowSizeDays int
	}
	if err := json.Unmarshal([]byte(f.bodies.Load().(string)), &body); err != nil {
		t.Fatal(err)
	}
	if body.Range.Start["date"] != (civilDate{2026, 10, 31}) || body.Range.End["date"] != (civilDate{2026, 11, 5}) || body.WindowSizeDays != 1 {
		t.Errorf("тело dailyRollUp = %+v", body)
	}
	if got := f.filters.Load().(string); got != `sleep.interval.civil_end_time >= "2026-10-30"` {
		t.Errorf("фильтр сна = %s", got)
	}
}

func TestGoogleUpdatesSkipsSameData(t *testing.T) {
	f := &fakeGoogle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := newGoogleSource(t, f).Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("первая выгрузка не пришла")
	}
	select {
	case <-ch:
		t.Fatal("те же данные отправлены повторно")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGoogleAuthAlert(t *testing.T) {
	f := &fakeGoogle{}
	f.status.Store(http.StatusUnauthorized)
	src := newGoogleSource(t, f)
	alerts := make(chan error, 10)
	src.Alert = func(_ context.Context, err error) { alerts <- err }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := src.Updates(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-alerts:
		if !errors.Is(err, ErrGoogleAuth) {
			t.Errorf("ошибка = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("нет оповещения о потере доступа")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(alerts); n != 0 {
		t.Errorf("оповещений сверх первого: %d, нужно не чаще раза в 12 часов", n)
	}
}

func TestGoogleServerErrorIsNotAuth(t *testing.T) {
	f := &fakeGoogle{}
	f.status.Store(http.StatusInternalServerError)
	_, err := newGoogleSource(t, f).Fetch(context.Background())
	if err == nil || errors.Is(err, ErrGoogleAuth) {
		t.Errorf("ошибка = %v", err)
	}
}

func TestTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "token.json")
	if _, err := LoadToken(path); err == nil {
		t.Error("нет файла — должна быть ошибка")
	}
	if err := SaveToken(path, &oauth2.Token{AccessToken: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(path); err == nil {
		t.Error("токен без refresh_token должен отклоняться")
	}
	if err := SaveToken(path, &oauth2.Token{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	tok, err := LoadToken(path)
	if err != nil || tok.RefreshToken != "r" {
		t.Errorf("токен = %+v, %v", tok, err)
	}
}

func TestGoogleClientWithoutToken(t *testing.T) {
	conf := GoogleOAuth("id", "secret")
	f := &fakeGoogle{}
	src := newGoogleSource(t, f)
	src.Client = GoogleClient(context.Background(), conf, filepath.Join(t.TempDir(), "none.json"))
	_, err := src.Fetch(context.Background())
	if !errors.Is(err, ErrGoogleAuth) {
		t.Errorf("без файла токена ожидается ErrGoogleAuth, получено %v", err)
	}
}
