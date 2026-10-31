package health

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// FileSource следит за файлом выгрузки в iCloud Drive опросом mtime:
// файлы iCloud плохо дружат с fsnotify (docs/SPEC.md, 4.1).
type FileSource struct {
	Path     string
	Interval time.Duration
	Log      *slog.Logger
}

// Updates читает файл сразу и затем при каждом изменении.
func (s *FileSource) Updates(ctx context.Context) (<-chan Export, error) {
	interval := s.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}
	out := make(chan Export)
	go func() {
		defer close(out)
		var last time.Time
		for {
			if exp, mod, ok := s.poll(ctx, last); ok {
				last = mod
				select {
				case out <- exp:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (s *FileSource) poll(ctx context.Context, last time.Time) (Export, time.Time, bool) {
	info, err := os.Stat(s.Path)
	if err != nil {
		s.requestDownload(ctx)
		return Export{}, last, false
	}
	if !info.ModTime().After(last) {
		return Export{}, last, false
	}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		s.Log.Warn("export_read_failed", "error", err)
		return Export{}, last, false
	}
	exp, err := Parse(raw)
	if err != nil {
		// mtime запоминаем: тот же битый файл не должен шуметь в лог каждые 30 секунд.
		s.Log.Warn("export_invalid", "error", err, "bytes", len(raw))
		return Export{}, info.ModTime(), false
	}
	return exp, info.ModTime(), true
}

// requestDownload просит iCloud скачать файл, если на диске лежит только заглушка.
func (s *FileSource) requestDownload(ctx context.Context) {
	placeholder := filepath.Join(filepath.Dir(s.Path), "."+filepath.Base(s.Path)+".icloud")
	if _, err := os.Stat(placeholder); err != nil {
		return
	}
	if err := exec.CommandContext(ctx, "brctl", "download", s.Path).Run(); err != nil {
		s.Log.Debug("icloud_download_failed", "error", err)
	}
}
