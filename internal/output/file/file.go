package file

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/model"
	"github.com/kaminocorp/lumber/internal/output"
)

const defaultBufSize = 64 * 1024 // 64KB

// Option configures a file Output.
type Option func(*Output)

// WithMaxSize sets the file size (bytes) at which rotation triggers.
// 0 (default) disables rotation.
func WithMaxSize(bytes int64) Option {
	return func(o *Output) { o.maxSize = bytes }
}

// WithBufSize sets the bufio.Writer buffer size. Default: 64KB.
func WithBufSize(bytes int) Option {
	return func(o *Output) { o.bufSize = bytes }
}

// Output writes NDJSON to a file with buffered I/O and optional size-based rotation.
type Output struct {
	w         *bufio.Writer
	f         *os.File
	mu        sync.Mutex
	path      string
	verbosity compactor.Verbosity
	maxSize   int64 // 0 = no rotation
	written   int64
	bufSize   int
}

// New creates a file output that writes NDJSON to the given path.
func New(path string, verbosity compactor.Verbosity, opts ...Option) (*Output, error) {
	o := &Output{
		path:      path,
		verbosity: verbosity,
		bufSize:   defaultBufSize,
	}
	for _, opt := range opts {
		opt(o)
	}
	if err := o.openFile(); err != nil {
		return nil, err
	}
	return o, nil
}

// Write JSON-encodes the event and appends it as a line to the file.
func (o *Output) Write(_ context.Context, event model.CanonicalEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	formatted := output.FormatEvent(event, o.verbosity)
	data, err := json.Marshal(formatted)
	if err != nil {
		return fmt.Errorf("file output: marshal: %w", err)
	}
	data = append(data, '\n')

	if o.maxSize > 0 && o.written+int64(len(data)) > o.maxSize {
		if err := o.rotate(); err != nil {
			return fmt.Errorf("file output: rotate: %w", err)
		}
	}

	n, err := o.w.Write(data)
	o.written += int64(n)
	if err != nil {
		return fmt.Errorf("file output: write: %w", err)
	}
	return nil
}

// Close flushes the buffer and closes the file.
func (o *Output) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.w.Flush(); err != nil {
		o.f.Close()
		return fmt.Errorf("file output: flush: %w", err)
	}
	return o.f.Close()
}

// openFile opens (or creates) the output file and wraps it in a bufio.Writer.
func (o *Output) openFile() error {
	f, err := os.OpenFile(o.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("file output: open %s: %w", o.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("file output: stat %s: %w", o.path, err)
	}
	o.f = f
	o.w = bufio.NewWriterSize(f, o.bufSize)
	o.written = info.Size()
	return nil
}

// rotate flushes, closes the current file, renames it to {path}.1
// (shifting existing rotated files), and opens a new file.
func (o *Output) rotate() error {
	if err := o.w.Flush(); err != nil {
		return err
	}
	if err := o.f.Close(); err != nil {
		return err
	}

	// Shift existing rotated files: .2 → .3, .1 → .2, current → .1
	for i := 9; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", o.path, i)
		to := fmt.Sprintf("%s.%d", o.path, i+1)
		os.Rename(from, to) // ignore errors — file may not exist
	}
	if err := os.Rename(o.path, o.path+".1"); err != nil {
		return err
	}

	o.written = 0
	return o.openFile()
}
