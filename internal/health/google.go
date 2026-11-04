package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// GoogleBaseURL — Google Health API (преемник Fitbit Web API).
const GoogleBaseURL = "https://health.googleapis.com"

// GoogleScopes — только чтение шагов и сна.
var GoogleScopes = []string{
	"https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly",
	"https://www.googleapis.com/auth/googlehealth.sleep.readonly",
}

// ErrGoogleAuth — доступ к Google потерян: нужен повторный `floor google-auth`.
var ErrGoogleAuth = errors.New("нет доступа к Google Health")

// GoogleSource опрашивает Google Health API и превращает ответ в ту же выгрузку
// schema = 1, что приходит с iPhone: дальше данные идут по общему пути.
type GoogleSource struct {
	Client   *http.Client
	BaseURL  string
	Loc      *time.Location
	Days     int           // сколько последних дней забирать
	Interval time.Duration // как часто опрашивать
	// RetryAfter — пауза после сетевой ошибки (по умолчанию минута):
	// Мак часто просыпается без сети на первые секунды.
	RetryAfter time.Duration
	Now        func() time.Time
	Log        *slog.Logger
	// Alert вызывается при потере доступа, не чаще раза в 12 часов.
	Alert func(ctx context.Context, err error)

	mu        sync.Mutex
	lastAlert time.Time
}

// Updates опрашивает API сразу и затем каждые Interval; повтор тех же данных не отправляет.
// После сетевой ошибки повторяет через минуту, после потери доступа — по обычному графику.
func (s *GoogleSource) Updates(ctx context.Context) (<-chan Export, error) {
	interval := s.Interval
	if interval == 0 {
		interval = 15 * time.Minute
	}
	retry := s.RetryAfter
	if retry == 0 {
		retry = time.Minute
	}
	out := make(chan Export)
	go func() {
		defer close(out)
		var last []byte
		for {
			wait := interval
			if exp, key, err := s.fetch(ctx); err != nil {
				s.fail(ctx, err)
				if !errors.Is(err, ErrGoogleAuth) && retry < interval {
					wait = retry
				}
			} else if !bytes.Equal(key, last) {
				select {
				case out <- exp:
					last = key
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Fetch забирает данные один раз: для `floor debug-day` и проверки после входа.
func (s *GoogleSource) Fetch(ctx context.Context) (Export, error) {
	exp, _, err := s.fetch(ctx)
	return exp, err
}

func (s *GoogleSource) fail(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	s.Log.Warn("google_fetch_failed", "error", err)
	if !errors.Is(err, ErrGoogleAuth) || s.Alert == nil {
		return
	}
	s.mu.Lock()
	now := s.now()
	due := now.Sub(s.lastAlert) >= 12*time.Hour
	if due {
		s.lastAlert = now
	}
	s.mu.Unlock()
	if due {
		s.Alert(ctx, err)
	}
}

func (s *GoogleSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type googleSteps struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

type googleSleep struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Value string `json:"value"`
}

type googleWorkout struct {
	ID      string `json:"id"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Type    string `json:"type"`
	Minutes int    `json:"minutes"`
}

// workouts — сессии тренировок, начатые с даты since.
func (s *GoogleSource) workouts(ctx context.Context, since time.Time) ([]googleWorkout, error) {
	q := url.Values{}
	q.Set("filter", fmt.Sprintf(`exercise.interval.civil_start_time >= "%s"`, since.Format(time.DateOnly)))
	q.Set("pageSize", "25")
	out := []googleWorkout{}
	for page := 0; page < 20; page++ {
		var resp struct {
			DataPoints []struct {
				Name     string `json:"name"`
				Exercise *struct {
					Interval       googleInterval `json:"interval"`
					ExerciseType   string         `json:"exerciseType"`
					ActiveDuration string         `json:"activeDuration"`
				} `json:"exercise"`
			} `json:"dataPoints"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := s.call(ctx, http.MethodGet, "/v4/users/me/dataTypes/exercise/dataPoints", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.DataPoints {
			if p.Exercise == nil {
				continue
			}
			start, end, ok := p.Exercise.Interval.local(s.Loc)
			if !ok {
				continue
			}
			w := googleWorkout{ID: p.Name, Start: start, End: end, Type: p.Exercise.ExerciseType}
			if d, err := time.ParseDuration(p.Exercise.ActiveDuration); err == nil {
				w.Minutes = int(d.Minutes())
			}
			out = append(out, w)
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		q.Set("pageToken", resp.NextPageToken)
	}
	return out, nil
}

// fetch возвращает выгрузку и ключ её содержимого без generated_at.
func (s *GoogleSource) fetch(ctx context.Context) (Export, []byte, error) {
	days := s.Days
	if days <= 0 {
		days = 5
	}
	now := s.now().In(s.Loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Loc)
	from := today.AddDate(0, 0, -(days - 1))

	steps, err := s.steps(ctx, from, today.AddDate(0, 0, 1))
	if err != nil {
		return Export{}, nil, fmt.Errorf("шаги: %w", err)
	}
	sleep, err := s.sleep(ctx, from.AddDate(0, 0, -1))
	if err != nil {
		return Export{}, nil, fmt.Errorf("сон: %w", err)
	}

	// Тренировки не обязательны: сбой здесь не должен стоить шагов и сна.
	workouts, err := s.workouts(ctx, from)
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("google_workouts_failed", "error", err)
		}
		workouts = []googleWorkout{}
	}

	key, err := json.Marshal(struct {
		Steps    []googleSteps   `json:"steps"`
		Sleep    []googleSleep   `json:"sleep"`
		Workouts []googleWorkout `json:"workouts"`
	}{steps, sleep, workouts})
	if err != nil {
		return Export{}, nil, err
	}
	raw, err := json.Marshal(map[string]any{
		"schema":       SchemaVersion,
		"source":       "google",
		"range_from":   from.Format(time.DateOnly),
		"range_to":     today.Format(time.DateOnly),
		"generated_at": now.Format(time.RFC3339),
		"tz_offset":    now.Format("-07:00"),
		"steps":        steps,
		"sleep":        sleep,
		"workouts":     workouts,
	})
	if err != nil {
		return Export{}, nil, err
	}
	exp, err := Parse(raw)
	return exp, key, err
}

type civilDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

func toCivil(t time.Time) map[string]civilDate {
	return map[string]civilDate{"date": {t.Year(), int(t.Month()), t.Day()}}
}

// steps — суммы шагов по местным датам через dailyRollUp.
func (s *GoogleSource) steps(ctx context.Context, from, to time.Time) ([]googleSteps, error) {
	body, err := json.Marshal(map[string]any{
		"range":          map[string]any{"start": toCivil(from), "end": toCivil(to)},
		"windowSizeDays": 1,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		RollupDataPoints []struct {
			CivilStartTime struct {
				Date civilDate `json:"date"`
			} `json:"civilStartTime"`
			Steps *struct {
				CountSum string `json:"countSum"`
			} `json:"steps"`
		} `json:"rollupDataPoints"`
	}
	if err := s.call(ctx, http.MethodPost, "/v4/users/me/dataTypes/steps/dataPoints:dailyRollUp", nil, body, &resp); err != nil {
		return nil, err
	}
	out := []googleSteps{}
	for _, p := range resp.RollupDataPoints {
		// Дня без синхронизации нет в ответе или у него нет суммы: не пишем ноль поверх реальных шагов.
		if p.Steps == nil || p.Steps.CountSum == "" {
			continue
		}
		n, err := strconv.ParseInt(p.Steps.CountSum, 10, 64)
		if err != nil {
			continue
		}
		d := p.CivilStartTime.Date
		out = append(out, googleSteps{Date: fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day), Count: n})
	}
	return out, nil
}

// Стадии Google → значения выгрузки. Беспокойный сон Fitbit сам не считает сном.
var googleStages = map[string]string{
	"ASLEEP":   ValueAsleep,
	"LIGHT":    ValueCore,
	"DEEP":     ValueDeep,
	"REM":      ValueREM,
	"AWAKE":    ValueAwake,
	"RESTLESS": ValueAwake,
}

type googleInterval struct {
	StartTime      string `json:"startTime"`
	EndTime        string `json:"endTime"`
	StartUTCOffset string `json:"startUtcOffset"`
	EndUTCOffset   string `json:"endUtcOffset"`
}

// local переводит интервал из UTC в местное время по смещениям из самого ответа
// (вид "18000s"); без смещения — в пояс по умолчанию. Метрики считают подъём
// по часам времени, поэтому UTC здесь дал бы подъём в 23:21 вместо 04:21.
func (iv googleInterval) local(def *time.Location) (string, string, bool) {
	start, err1 := time.Parse(time.RFC3339Nano, iv.StartTime)
	end, err2 := time.Parse(time.RFC3339Nano, iv.EndTime)
	if err1 != nil || err2 != nil {
		return "", "", false
	}
	return start.In(offsetZone(iv.StartUTCOffset, def)).Format(time.RFC3339),
		end.In(offsetZone(iv.EndUTCOffset, def)).Format(time.RFC3339), true
}

func offsetZone(offset string, def *time.Location) *time.Location {
	d, err := time.ParseDuration(offset)
	if err != nil || offset == "" {
		return def
	}
	return time.FixedZone("", int(d.Seconds()))
}

// sleep — сегменты сна для сессий, закончившихся с даты since.
func (s *GoogleSource) sleep(ctx context.Context, since time.Time) ([]googleSleep, error) {
	q := url.Values{}
	q.Set("filter", fmt.Sprintf(`sleep.interval.civil_end_time >= "%s"`, since.Format(time.DateOnly)))
	q.Set("pageSize", "25")
	out := []googleSleep{}
	for page := 0; page < 20; page++ {
		var resp struct {
			DataPoints []struct {
				Sleep *struct {
					Interval googleInterval `json:"interval"`
					Stages   []struct {
						googleInterval
						Type string `json:"type"`
					} `json:"stages"`
				} `json:"sleep"`
			} `json:"dataPoints"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := s.call(ctx, http.MethodGet, "/v4/users/me/dataTypes/sleep/dataPoints", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.DataPoints {
			if p.Sleep == nil {
				continue
			}
			if len(p.Sleep.Stages) == 0 {
				if start, end, ok := p.Sleep.Interval.local(s.Loc); ok {
					out = append(out, googleSleep{start, end, ValueAsleep})
				}
				continue
			}
			for _, st := range p.Sleep.Stages {
				v, ok := googleStages[st.Type]
				if !ok {
					continue
				}
				if start, end, ok := st.local(s.Loc); ok {
					out = append(out, googleSleep{start, end, v})
				}
			}
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		q.Set("pageToken", resp.NextPageToken)
	}
	return out, nil
}

func (s *GoogleSource) call(ctx context.Context, method, path string, q url.Values, body []byte, dst any) error {
	base := s.BaseURL
	if base == "" {
		base = GoogleBaseURL
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) || errors.Is(err, ErrGoogleAuth) {
			return fmt.Errorf("%w: %v", ErrGoogleAuth, err)
		}
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: HTTP %d: %s", ErrGoogleAuth, resp.StatusCode, snippet(raw))
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	return json.Unmarshal(raw, dst)
}

func snippet(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return string(bytes.TrimSpace(b))
}

// GoogleOAuth — OAuth-клиент типа «Desktop app»: вход через 127.0.0.1.
func GoogleOAuth(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       GoogleScopes,
	}
}

// googleTimeout — предел одного запроса: зависшая сеть не должна останавливать опрос.
const googleTimeout = time.Minute

// GoogleClient — HTTP-клиент с токеном из файла; обновлённый токен сохраняется обратно.
func GoogleClient(ctx context.Context, conf *oauth2.Config, tokenFile string) *http.Client {
	// Тот же предел и для обновления токена: oauth2 берёт клиента из контекста.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: googleTimeout})
	ts := &fileTokenSource{conf: conf, path: tokenFile, ctx: ctx}
	c := oauth2.NewClient(ctx, ts)
	c.Timeout = googleTimeout
	return c
}

type fileTokenSource struct {
	conf *oauth2.Config
	path string
	ctx  context.Context

	mu  sync.Mutex
	src oauth2.TokenSource
	tok *oauth2.Token
}

// Token читает файл при первом вызове: вход можно пройти уже после запуска бота.
func (f *fileTokenSource) Token() (*oauth2.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.src == nil {
		tok, err := LoadToken(f.path)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrGoogleAuth, err)
		}
		f.tok = tok
		f.src = f.conf.TokenSource(f.ctx, tok)
	}
	tok, err := f.src.Token()
	if err != nil {
		// Следующий вызов перечитает файл: вдруг вход уже пройден заново.
		f.src = nil
		return nil, fmt.Errorf("%w: %v", ErrGoogleAuth, err)
	}
	if tok.AccessToken != f.tok.AccessToken {
		f.tok = tok
		_ = SaveToken(f.path, tok)
	}
	return tok, nil
}

// LoadToken читает токен, сохранённый `floor google-auth`.
func LoadToken(path string) (*oauth2.Token, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if tok.RefreshToken == "" {
		return nil, fmt.Errorf("%s: нет refresh_token", path)
	}
	return &tok, nil
}

// SaveToken пишет токен атомарно и с правами 600.
func SaveToken(path string, tok *oauth2.Token) error {
	raw, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
