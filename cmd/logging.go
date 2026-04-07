package cmd

import (
	"fmt"
	"os"
	"sync"
)

// RotatingWriter is an io.Writer that rotates the underlying file when it
// exceeds maxSize bytes. Old files are renamed with a numeric suffix
// (e.g. server.log.1, server.log.2) up to maxFiles backups.
type RotatingWriter struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	maxSize  int64
	maxFiles int
	size     int64
}

// NewRotatingWriter opens (or creates) the log file and returns a writer
// that rotates it when it exceeds maxSize.
func NewRotatingWriter(path string, maxSize int64, maxFiles int) (*RotatingWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &RotatingWriter{
		file:     f,
		path:     path,
		maxSize:  maxSize,
		maxFiles: maxFiles,
		size:     info.Size(),
	}, nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) rotate() {
	w.file.Close()

	// server.log.3 → deleted
	// server.log.2 → server.log.3
	// server.log.1 → server.log.2
	// server.log   → server.log.1
	for i := w.maxFiles; i >= 1; i-- {
		target := fmt.Sprintf("%s.%d", w.path, i)
		if i == w.maxFiles {
			os.Remove(target)
		}
		source := w.path
		if i > 1 {
			source = fmt.Sprintf("%s.%d", w.path, i-1)
		}
		os.Rename(source, target)
	}

	w.file, _ = os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	w.size = 0
}

// Close closes the underlying file.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
