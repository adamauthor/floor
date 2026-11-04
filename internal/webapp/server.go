package webapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxAge — сколько живёт подпись страницы: окно настроек дольше суток не держат.
const MaxAge = 24 * time.Hour

// CheckInitData проверяет подпись Telegram у Telegram.WebApp.initData и возвращает id
// пользователя. Ключ подписи выводится из токена бота, подделать его без токена нельзя.
func CheckInitData(initData, token string, now time.Time) (int64, error) {
	vals, err := url.ParseQuery(initData)
	if err != nil || initData == "" {
		return 0, errors.New("нет подписи Telegram")
	}
	got := vals.Get("hash")
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + vals.Get(k)
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(want)) {
		return 0, errors.New("подпись Telegram не сходится")
	}

	auth, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil || now.Sub(time.Unix(auth, 0)) > MaxAge {
		return 0, errors.New("подпись устарела: открой настройки заново")
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(vals.Get("user")), &user); err != nil || user.ID == 0 {
		return 0, errors.New("в подписи нет пользователя")
	}
	return user.ID, nil
}

// Server принимает сохранение со страницы, открытой из кнопки меню: оттуда
// sendData недоступен, поэтому страница шлёт настройки сюда через туннель.
type Server struct {
	Token  string
	Owner  int64
	Origin string // откуда разрешены запросы, например https://adamauthor.github.io
	Save   func(ctx context.Context, data string) (text string, ok bool)
	Log    *slog.Logger
	Now    func() time.Time
}

type saveRequest struct {
	Init string `json:"init"`
	Data string `json:"data"`
}

type saveResponse struct {
	OK   bool   `json:"ok"`
	Text string `json:"text"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") == s.Origin {
		w.Header().Set("Access-Control-Allow-Origin", s.Origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Vary", "Origin")
	}
	switch {
	case r.Method == http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
		return
	case r.URL.Path != "/settings":
		http.NotFound(w, r)
		return
	case r.Method != http.MethodPost:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req saveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		s.reply(w, http.StatusBadRequest, saveResponse{Text: "Не разобрал запрос."})
		return
	}
	user, err := CheckInitData(req.Init, s.Token, s.Now())
	if err == nil && user != s.Owner {
		err = errors.New("чужой пользователь")
	}
	if err != nil {
		s.Log.Warn("webapp_rejected", "error", err)
		s.reply(w, http.StatusForbidden, saveResponse{Text: "Не сохранил: " + err.Error()})
		return
	}
	text, ok := s.Save(r.Context(), req.Data)
	s.reply(w, http.StatusOK, saveResponse{OK: ok, Text: text})
}

func (s *Server) reply(w http.ResponseWriter, code int, resp saveResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}

// Origin — схема и хост адреса страницы.
func Origin(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Tunnel узнаёт публичный адрес у локального агента ngrok: на бесплатном тарифе он
// меняется при каждом перезапуске туннеля.
type Tunnel struct {
	API    string // локальный API агента, обычно http://127.0.0.1:4040
	Local  string // адрес, который туннель пробрасывает, например 127.0.0.1:8791
	Client *http.Client
}

// PublicURL — https-адрес туннеля к Local или ошибка, если туннель не поднят.
func (t Tunnel) PublicURL(ctx context.Context) (string, error) {
	_, port, err := net.SplitHostPort(t.Local)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.API, "/")+"/api/tunnels", nil)
	if err != nil {
		return "", err
	}
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ngrok: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Tunnels []struct {
			PublicURL string `json:"public_url"`
			Config    struct {
				Addr string `json:"addr"`
			} `json:"config"`
		} `json:"tunnels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("ngrok: %w", err)
	}
	for _, tn := range body.Tunnels {
		if strings.HasPrefix(tn.PublicURL, "https://") && strings.HasSuffix(tn.Config.Addr, ":"+port) {
			return tn.PublicURL, nil
		}
	}
	return "", errors.New("ngrok: нет туннеля к " + t.Local)
}
