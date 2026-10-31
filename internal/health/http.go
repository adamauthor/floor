package health

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// MaxBodyBytes — предел тела POST /ingest.
const MaxBodyBytes = 1 << 20

// HTTPSource принимает выгрузки через POST /ingest (режим Fly.io).
type HTTPSource struct {
	Addr  string
	Token string
	Log   *slog.Logger
}

// Updates поднимает HTTP-сервер; он живёт до отмены ctx.
func (s *HTTPSource) Updates(ctx context.Context) (<-chan Export, error) {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return nil, fmt.Errorf("ingest listen: %w", err)
	}
	out := make(chan Export)
	srv := &http.Server{Handler: s.Handler(ctx, out), ReadHeaderTimeout: 10 * time.Second}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.Log.Error("ingest_server_failed", "error", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		<-done
		close(out)
	}()
	return out, nil
}

// Handler — маршруты сервера; 204 означает, что выгрузка принята в обработку.
func (s *HTTPSource) Handler(ctx context.Context, out chan<- Export) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest", func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + s.Token
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		exp, err := Parse(raw)
		if err != nil {
			s.Log.Warn("export_invalid", "error", err, "bytes", len(raw))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case out <- exp:
			w.WriteHeader(http.StatusNoContent)
		case <-ctx.Done():
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	return mux
}
