// Package download owns background jobs independently of the HTTP API.
package download

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	StatusQueued          = "queued"
	StatusRunning         = "running"
	StatusCompleted       = "completed"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
	queueCapacity         = 8
	maxJobs               = 32
	jobTTL                = time.Hour
	jobTimeout            = 15 * time.Minute
	maxJobBytes     int64 = 1 << 30
)

type Request struct{ URL, Quality, Mode, AudioFormat string }
type Job struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	Filename  string    `json:"filename,omitempty"`
	Expires   time.Time `json:"expiresAt,omitzero"`
	Path      string    `json:"-"`
	MediaType string    `json:"-"`
}
type Runner interface {
	Run(context.Context, Request, string) (string, error)
}

var ErrFull = errors.New("download queue is full")

type entry struct {
	Job
	request Request
	ctx     context.Context
	cancel  context.CancelFunc
	active  bool
}
type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*entry
	queue  chan *entry
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	root   string
	runner Runner
}

func New(root string, runner Runner) (*Manager, error) {
	// A private per-process directory makes ownership explicit. Jobs are ephemeral.
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = os.MkdirTemp(root, "session-")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{jobs: map[string]*entry{}, queue: make(chan *entry, queueCapacity), ctx: ctx, cancel: cancel, root: root, runner: runner}
	m.wg.Add(2)
	go m.loop()
	go m.cleanLoop()
	return m, nil
}

// Close stops subprocesses before removing the private session directory.
func (m *Manager) Close() error {
	m.cancel()
	m.wg.Wait()
	return os.RemoveAll(m.root)
}

func (m *Manager) Submit(r Request) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil || len(m.jobs) >= maxJobs {
		return Job{}, ErrFull
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Job{}, err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	e := &entry{Job: Job{ID: hex.EncodeToString(id[:]), Status: StatusQueued}, request: r, ctx: ctx, cancel: cancel}
	select {
	case m.queue <- e:
		m.jobs[e.ID] = e
		return e.Job, nil
	default:
		cancel()
		return Job{}, ErrFull
	}
}
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok || expired(e, time.Now()) {
		return Job{}, false
	}
	return e.Job, true
}

// Cancel returns the resulting snapshot atomically, including terminal jobs.
func (m *Manager) Cancel(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok || expired(e, time.Now()) {
		return Job{}, false
	}
	if e.Status == StatusQueued || e.Status == StatusRunning {
		e.cancel()
		e.Status = StatusCancelled
		e.Expires = time.Now().Add(jobTTL)
	}
	return e.Job, true
}

func expired(e *entry, now time.Time) bool {
	return !e.Expires.IsZero() && !now.Before(e.Expires)
}

func (m *Manager) loop() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case e := <-m.queue:
			m.run(e)
		}
	}
}

func (m *Manager) cleanLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			m.cleanup(now)
		}
	}
}

// Do filesystem work outside the mutex so polling cannot block on deletion.
func (m *Manager) cleanup(now time.Time) {
	var dirs []string
	m.mu.Lock()
	for id, e := range m.jobs {
		if !e.active && expired(e, now) {
			e.cancel()
			delete(m.jobs, id)
			dirs = append(dirs, filepath.Join(m.root, id))
		}
	}
	m.mu.Unlock()
	for _, dir := range dirs {
		_ = os.RemoveAll(dir)
	}
}

func (m *Manager) run(e *entry) {
	m.mu.Lock()
	if e.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	e.Status = StatusRunning
	e.active = true
	m.mu.Unlock()
	dir := filepath.Join(m.root, e.ID)
	err := os.Mkdir(dir, 0700)
	var file string
	if err == nil {
		ctx, cancel := context.WithTimeout(e.ctx, jobTimeout)
		file, err = m.runner.Run(ctx, e.request, dir)
		cancel()
	}
	// Validate the result before publishing it; never expose a partial file.
	status, message := StatusCompleted, ""
	if err != nil {
		status, message = StatusFailed, "Download failed. The source may require authentication or be unavailable."
		if errors.Is(err, context.DeadlineExceeded) {
			message = "Download exceeded the time limit."
		}
	} else {
		info, statErr := os.Lstat(file)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxJobBytes || filepath.Dir(file) != dir {
			status, message = StatusFailed, "The downloader returned no usable media."
		}
	}
	m.mu.Lock()
	if e.ctx.Err() != nil {
		status, message = StatusCancelled, ""
	}
	e.Status, e.Error = status, message
	e.Expires = time.Now().Add(jobTTL)
	if status == StatusCompleted {
		e.Path, e.Filename = file, filepath.Base(file)
		e.MediaType = "video"
		if e.request.Mode == "audio" {
			e.MediaType = "audio"
		}
	}
	m.mu.Unlock()
	if status != StatusCompleted {
		_ = os.RemoveAll(dir)
	}
	e.cancel()
	m.mu.Lock()
	e.active = false
	m.mu.Unlock()
}
