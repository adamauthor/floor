// Package tgtest — поддельный сервер Telegram Bot API для тестов: настоящая библиотека
// ходит по HTTP, сервер записывает вызовы и отдаёт заранее поставленные обновления.
package tgtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Call — один записанный вызов метода API.
type Call struct {
	Method string
	Params map[string]string
}

// Server — поддельный Bot API.
type Server struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []Call
	updates []string
	nextID  int
	nextUpd int
	failing map[string]string // метод → текст ошибки
}

// New запускает сервер.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{nextID: 100, nextUpd: 1, failing: make(map[string]string)}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

// Fail заставляет метод отвечать ошибкой 400 с данным описанием.
func (s *Server) Fail(method, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing[method] = description
}

// PushUpdate ставит обновление в очередь; update — JSON без update_id.
func (s *Server) PushUpdate(update string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, fmt.Sprintf(`{"update_id":%d,%s`, s.nextUpd, strings.TrimPrefix(update, "{")))
	s.nextUpd++
}

// PushText ставит текстовое сообщение от пользователя.
func (s *Server) PushText(from int64, text string) {
	raw, _ := json.Marshal(text)
	s.PushUpdate(fmt.Sprintf(`{"message":{"message_id":1,"date":1,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"x"},"text":%s}}`, from, from, raw))
}

// PushVoice ставит голосовое сообщение.
func (s *Server) PushVoice(from int64) {
	s.PushUpdate(fmt.Sprintf(`{"message":{"message_id":2,"date":1,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"x"},"voice":{"file_id":"f","file_unique_id":"u","duration":1}}}`, from, from))
}

// PushCallback ставит нажатие кнопки.
func (s *Server) PushCallback(from int64, msgID int, data string) {
	s.PushUpdate(fmt.Sprintf(`{"callback_query":{"id":"cb1","chat_instance":"c","data":%q,"from":{"id":%d,"is_bot":false,"first_name":"x"},"message":{"message_id":%d,"date":1,"chat":{"id":%d,"type":"private"}}}}`, data, from, msgID, from))
}

// Calls возвращает вызовы метода (пустой method — все, кроме getUpdates).
func (s *Server) Calls(method string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if (method == "" && c.Method != "getUpdates") || c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// WaitCalls ждёт, пока метод будет вызван минимум n раз.
func (s *Server) WaitCalls(t *testing.T, method string, n int) []Call {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if calls := s.Calls(method); len(calls) >= n {
			return calls
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("метод %s вызван %d раз, ожидалось минимум %d", method, len(s.Calls(method)), n)
	return nil
}

// WaitText ждёт отправленное сообщение с точным текстом: фоновый тик может отправить
// что-то своё в любой момент, поэтому порядок сообщений тестам не гарантирован.
func (s *Server) WaitText(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range s.Calls("sendMessage") {
			if c.Params["text"] == text {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("сообщение %q не отправлено", text)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	method := parts[len(parts)-1]
	_ = r.ParseMultipartForm(1 << 20)
	params := make(map[string]string)
	if r.MultipartForm != nil {
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				params[k] = v[0]
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if method == "getUpdates" {
		var pending []string
		pending, s.updates = s.updates, nil
		if len(pending) == 0 {
			s.mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			s.mu.Lock()
		}
		fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, strings.Join(pending, ","))
		return
	}
	s.calls = append(s.calls, Call{Method: method, Params: params})
	if desc, ok := s.failing[method]; ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": desc})
		return
	}
	switch method {
	case "sendMessage", "editMessageText":
		s.nextID++
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":1,"type":"private"}}}`, s.nextID)
	default:
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}
}
