package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"floor/internal/core"
	"floor/internal/tgtest"
)

const (
	owner    = int64(42)
	stranger = int64(666)
	token    = "123:SECRET-TOKEN"
)

type fakeHandler struct {
	mu       sync.Mutex
	texts    []string
	nonText  int
	starts   int
	callback string
	answer   string
	settings int
	webapp   []string
}

func (h *fakeHandler) OnCallback(_ context.Context, data string, _ int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callback = data
	return h.answer
}
func (h *fakeHandler) OnText(_ context.Context, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.texts = append(h.texts, text)
}
func (h *fakeHandler) OnNonText(context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nonText++
}
func (h *fakeHandler) OnSettings(context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.settings++
}
func (h *fakeHandler) OnWebAppData(_ context.Context, data string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.webapp = append(h.webapp, data)
}
func (h *fakeHandler) OnStart(context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts++
}

// safeBuffer — лог, который пишет фоновый long polling и читает тест.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func setup(t *testing.T) (*Bot, *tgtest.Server, *fakeHandler, *safeBuffer) {
	t.Helper()
	srv := tgtest.New(t)
	logs := &safeBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, err := New(token, owner, srv.URL, log)
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHandler{answer: "ответ"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Start(ctx, h)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return b, srv, h, logs
}

func TestSendWithButtons(t *testing.T) {
	b, srv, _, _ := setup(t)
	id, err := b.Send(context.Background(), "<b>текст</b>", []core.Button{{Text: "✅ Да", Data: "floor:2026-10-31:yes"}})
	if err != nil || id == 0 {
		t.Fatalf("id = %d, err = %v", id, err)
	}
	c := srv.Calls("sendMessage")[0].Params
	if c["chat_id"] != "42" || c["parse_mode"] != "HTML" || !strings.Contains(c["reply_markup"], "floor:2026-10-31:yes") {
		t.Errorf("параметры: %v", c)
	}
}

func TestEditErrors(t *testing.T) {
	b, srv, _, _ := setup(t)
	ctx := context.Background()
	if err := b.Edit(ctx, 5, "x"); err != nil {
		t.Fatal(err)
	}
	if c := srv.Calls("editMessageText")[0].Params; strings.Contains(c["reply_markup"], "inline_keyboard") {
		t.Errorf("кнопки должны исчезать: %v", c)
	}
	srv.Fail("editMessageText", "Bad Request: message is not modified")
	if err := b.Edit(ctx, 5, "x"); err != nil {
		t.Errorf("неизменённое сообщение — не ошибка: %v", err)
	}
	srv.Fail("editMessageText", "Bad Request: message to edit not found")
	if err := b.Edit(ctx, 5, "x"); !errors.Is(err, core.ErrMessageGone) {
		t.Errorf("err = %v", err)
	}
	srv.Fail("editMessageText", "Bad Request: что-то другое")
	if err := b.Edit(ctx, 5, "x"); err == nil || errors.Is(err, core.ErrMessageGone) {
		t.Errorf("err = %v", err)
	}
}

func TestPinAndTyping(t *testing.T) {
	b, srv, _, _ := setup(t)
	if err := b.Pin(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if c := srv.Calls("pinChatMessage")[0].Params; c["message_id"] != "7" || c["disable_notification"] != "true" {
		t.Errorf("pin: %v", c)
	}
	if err := b.Typing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := srv.Calls("sendChatAction")[0].Params; c["action"] != "typing" {
		t.Errorf("typing: %v", c)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("условие не выполнилось")
}

func TestOwnerRouting(t *testing.T) {
	_, srv, h, _ := setup(t)

	srv.PushText(owner, "  привет  ")
	srv.PushVoice(owner)
	srv.PushText(owner, "/start")
	srv.PushCallback(owner, 9, "floor:2026-10-31:yes")

	waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.texts) == 1 && h.nonText == 1 && h.starts == 1 && h.callback != ""
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.texts[0] != "привет" || h.callback != "floor:2026-10-31:yes" {
		t.Errorf("h = %+v", h)
	}
	if c := srv.WaitCalls(t, "answerCallbackQuery", 1)[0].Params; c["text"] != "ответ" {
		t.Errorf("answer: %v", c)
	}
}

func TestStrangersIgnoredSilently(t *testing.T) {
	_, srv, h, logs := setup(t)

	srv.PushText(stranger, "секрет от чужого")
	srv.PushVoice(stranger)
	srv.PushCallback(stranger, 9, "floor:2026-10-31:yes")
	srv.PushText(owner, "от владельца") // как только дошло это, чужие уже обработаны или в работе

	waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.texts) == 1
	})
	time.Sleep(50 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.texts[0] != "от владельца" || h.nonText != 0 || h.callback != "" {
		t.Errorf("чужое дошло до обработчика: %+v", h)
	}
	if calls := srv.Calls(""); len(calls) != 0 {
		t.Errorf("чужим нельзя отвечать: %+v", calls)
	}
	if strings.Contains(logs.String(), "секрет") {
		t.Error("текст сообщения попал в лог")
	}
}

func TestErrorsDoNotLeakToken(t *testing.T) {
	logs := &safeBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	// Порт закрыт: ошибка сети содержит полный URL с токеном.
	b, err := New(token, owner, "http://127.0.0.1:1", log)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Send(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Errorf("токен в ошибке: %v", err)
	}
	if strings.Contains(b.scrub(errors.New("url /bot"+token+"/x")).Error(), token) {
		t.Error("scrub не убрал токен")
	}
	if b.scrub(nil) != nil {
		t.Error("scrub(nil) должен быть nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Start(ctx, &fakeHandler{})
	}()
	waitFor(t, func() bool { return strings.Contains(logs.String(), "telegram_error") })
	cancel()
	<-done
	if strings.Contains(logs.String(), "SECRET-TOKEN") {
		t.Errorf("токен в логе:\n%s", logs.String())
	}
}

func TestSettingsRouting(t *testing.T) {
	_, srv, h, _ := setup(t)
	srv.PushText(owner, "/settings")
	srv.PushUpdate(fmt.Sprintf(`{"message":{"message_id":3,"date":1,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"x"},"web_app_data":{"data":"{\"v\":1}","button_text":"⚙️ Настройки"}}}`, owner, owner))
	srv.PushUpdate(fmt.Sprintf(`{"message":{"message_id":4,"date":1,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"x"},"web_app_data":{"data":"чужое","button_text":"x"}}}`, stranger, stranger))

	waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.settings == 1 && len(h.webapp) == 1
	})
	time.Sleep(50 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.webapp[0] != `{"v":1}` || h.nonText != 0 || len(h.webapp) != 1 {
		t.Errorf("h = %+v", h)
	}
}

func TestMenuAppAndSetup(t *testing.T) {
	b, srv, _, _ := setup(t)
	ctx := context.Background()
	if err := b.SetMenuApp(ctx, "⚙️ Настройки", "https://example.test/app/#zAA"); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("setChatMenuButton")[0].Params
	for _, want := range []string{`"type":"web_app"`, `"web_app":{"url":"https://example.test/app/#zAA"}`} {
		if !strings.Contains(c["menu_button"], want) {
			t.Errorf("нет %s в %s", want, c["menu_button"])
		}
	}
	if c["chat_id"] != fmt.Sprint(owner) {
		t.Errorf("chat_id = %s", c["chat_id"])
	}
	if err := b.RemoveKeyboard(ctx, "текст"); err != nil {
		t.Fatal(err)
	}
	if m := srv.Calls("sendMessage")[0].Params["reply_markup"]; !strings.Contains(m, `"remove_keyboard":true`) {
		t.Errorf("reply_markup: %s", m)
	}
	if err := b.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if c := srv.Calls("setMyCommands")[0].Params["commands"]; !strings.Contains(c, `"settings"`) {
		t.Errorf("команды: %s", c)
	}
	if len(Description) > 512 || len([]rune(ShortDescription)) > 120 {
		t.Error("тексты профиля длиннее лимитов Telegram")
	}
	for m, n := range map[string]int{"setMyDescription": 1, "setMyShortDescription": 1, "setChatMenuButton": 2} {
		if len(srv.Calls(m)) != n {
			t.Errorf("%s не вызван", m)
		}
	}
}
