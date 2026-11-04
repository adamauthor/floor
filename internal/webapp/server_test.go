package webapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const token = "123:SECRET"

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// sign собирает initData так, как его подписывает Telegram.
func sign(user int64, at time.Time) string {
	vals := url.Values{
		"auth_date": {strconv.FormatInt(at.Unix(), 10)},
		"user":      {`{"id":` + strconv.FormatInt(user, 10) + `,"first_name":"А"}`},
		"signature": {"ignored-but-signed"},
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		lines = append(lines, k+"="+vals.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	vals.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return vals.Encode()
}

func TestCheckInitData(t *testing.T) {
	if id, err := CheckInitData(sign(42, now.Add(-time.Hour)), token, now); err != nil || id != 42 {
		t.Fatalf("валидная подпись: %d, %v", id, err)
	}
	good := sign(42, now)
	for name, data := range map[string]string{
		"пусто":         "",
		"чужой токен":   strings.Replace(good, "hash=", "hash=00", 1),
		"подменён user": strings.Replace(good, "42", "43", 1),
		"устарела":      sign(42, now.Add(-MaxAge-time.Minute)),
	} {
		if _, err := CheckInitData(data, token, now); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
}

func TestServer(t *testing.T) {
	var saved []string
	srv := &Server{
		Token: token, Owner: 42, Origin: "https://pages.test",
		Save: func(_ context.Context, data string) (string, bool) {
			saved = append(saved, data)
			return "✅ Сохранено", true
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now },
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", "https://pages.test")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	body := func(init string) string {
		raw, _ := json.Marshal(saveRequest{Init: init, Data: `{"v":1}`})
		return string(raw)
	}

	if w := do(http.MethodOptions, "/settings", ""); w.Code != http.StatusNoContent ||
		w.Header().Get("Access-Control-Allow-Origin") != "https://pages.test" {
		t.Errorf("preflight: %d %v", w.Code, w.Header())
	}
	if w := do(http.MethodPost, "/settings", body(sign(7, now))); w.Code != http.StatusForbidden {
		t.Errorf("чужой: %d", w.Code)
	}
	if w := do(http.MethodPost, "/settings", body("hash=x")); w.Code != http.StatusForbidden {
		t.Errorf("без подписи: %d", w.Code)
	}
	if w := do(http.MethodPost, "/other", body(sign(42, now))); w.Code != http.StatusNotFound {
		t.Errorf("путь: %d", w.Code)
	}
	if len(saved) != 0 {
		t.Fatal("сохранено без проверки")
	}
	w := do(http.MethodPost, "/settings", body(sign(42, now)))
	var resp saveResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil || !resp.OK || resp.Text != "✅ Сохранено" {
		t.Fatalf("ответ: %d %+v %v", w.Code, resp, err)
	}
	if len(saved) != 1 || saved[0] != `{"v":1}` {
		t.Errorf("сохранено: %v", saved)
	}
}

func TestTunnelPublicURL(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"tunnels":[
			{"public_url":"https://other.ngrok.test","config":{"addr":"http://localhost:3000"}},
			{"public_url":"https://floor.ngrok.test","config":{"addr":"http://127.0.0.1:8791"}}]}`)
	}))
	defer api.Close()
	got, err := Tunnel{API: api.URL, Local: "127.0.0.1:8791"}.PublicURL(context.Background())
	if err != nil || got != "https://floor.ngrok.test" {
		t.Errorf("адрес %q, %v", got, err)
	}
	if _, err := (Tunnel{API: api.URL, Local: "127.0.0.1:9999"}).PublicURL(context.Background()); err == nil {
		t.Error("чужой туннель принят")
	}
	if Origin("https://adamauthor.github.io/floor/app/") != "https://adamauthor.github.io" {
		t.Error("Origin")
	}
}
