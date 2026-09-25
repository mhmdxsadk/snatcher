package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type runnerFunc func(context.Context, Request, string) (string, error)

func (f runnerFunc) Run(c context.Context, r Request, d string) ([]string, error) {
	p, err := f(c, r, d)
	return []string{p}, err
}
func awaitStatus(t *testing.T, m *Manager, id, status string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, _ := m.Get(id)
		if j.Status == status {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	j, _ := m.Get(id)
	t.Fatalf("wanted %s, got %+v", status, j)
	return Job{}
}
func TestCompletionAndCleanup(t *testing.T) {
	m, err := New(t.TempDir(), runnerFunc(func(_ context.Context, _ Request, d string) (string, error) {
		p := filepath.Join(d, "test.mp4")
		return p, os.WriteFile(p, []byte("media"), 0600)
	}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.Submit(Request{})
	if err != nil {
		t.Fatal(err)
	}
	j = awaitStatus(t, m, j.ID, "completed")
	if j.Filename != "test.mp4" {
		t.Fatal(j)
	}
	m.Close()
	if _, err := os.Stat(j.Files[0]); !os.IsNotExist(err) {
		t.Fatal("file survived close")
	}
}
func TestCancelRunningAndQueued(t *testing.T) {
	started := make(chan struct{})
	m, err := New(t.TempDir(), runnerFunc(func(c context.Context, _ Request, d string) (string, error) {
		close(started)
		<-c.Done()
		return "", c.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	a, _ := m.Submit(Request{})
	<-started
	b, _ := m.Submit(Request{})
	m.Cancel(b.ID)
	m.Cancel(a.ID)
	awaitStatus(t, m, a.ID, "cancelled")
	awaitStatus(t, m, b.ID, "cancelled")
}
func TestRejectOutsideFile(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, []byte("private"), 0600)
	m, err := New(t.TempDir(), runnerFunc(func(context.Context, Request, string) (string, error) { return outside, nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, _ := m.Submit(Request{})
	awaitStatus(t, m, j.ID, "failed")
}
func TestCapacity(t *testing.T) {
	m, err := New(t.TempDir(), runnerFunc(func(c context.Context, _ Request, _ string) (string, error) { <-c.Done(); return "", c.Err() }))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	full := false
	for i := 0; i < 12; i++ {
		if _, err := m.Submit(Request{}); err == ErrFull {
			full = true
			break
		}
	}
	if !full {
		t.Fatal("queue unbounded")
	}
}

// Cleanup must not expire queued jobs or wait for an unrelated active download.
func TestCleanupWhileRunnerBusy(t *testing.T) {
	started := make(chan struct{})
	m, err := New(t.TempDir(), runnerFunc(func(ctx context.Context, _ Request, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	running, err := m.Submit(Request{})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	queued, err := m.Submit(Request{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.root, "expired")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.jobs["expired"] = &entry{Job: Job{ID: "expired", Status: StatusCompleted, Expires: time.Now().Add(-time.Minute)}, ctx: ctx, cancel: cancel}
	m.mu.Unlock()
	m.cleanup(time.Now().Add(2 * time.Hour))
	if _, ok := m.Get(running.ID); !ok {
		t.Fatal("running job expired")
	}
	if j, ok := m.Get(queued.ID); !ok || !j.Expires.IsZero() {
		t.Fatal("queued job expired")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expired directory survived")
	}
	if _, ok := m.Cancel("expired"); ok {
		t.Fatal("cancel accepted expired job")
	}
	snapshot, ok := m.Cancel(queued.ID)
	if !ok || snapshot.ID != queued.ID || snapshot.Status != StatusCancelled {
		t.Fatalf("invalid cancel snapshot: %+v", snapshot)
	}
}

func TestDeadlineFailure(t *testing.T) {
	m, err := New(t.TempDir(), runnerFunc(func(context.Context, Request, string) (string, error) { return "", context.DeadlineExceeded }))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	job, err := m.Submit(Request{})
	if err != nil {
		t.Fatal(err)
	}
	job = awaitStatus(t, m, job.ID, StatusFailed)
	if job.Error != "Download exceeded the time limit." {
		t.Fatal(job.Error)
	}
}

func TestAudioContainerClassification(t *testing.T) {
	m, err := New(t.TempDir(), runnerFunc(func(_ context.Context, _ Request, dir string) (string, error) {
		path := filepath.Join(dir, "audio.webm")
		return path, os.WriteFile(path, []byte("audio"), 0600)
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	job, err := m.Submit(Request{Mode: "audio", AudioFormat: "best"})
	if err != nil {
		t.Fatal(err)
	}
	job = awaitStatus(t, m, job.ID, StatusCompleted)
	if job.MediaType != "audio" {
		t.Fatal("audio-only WebM classified as video")
	}
}

type filesRunner func(context.Context, Request, string) ([]string, error)

func (f filesRunner) Run(ctx context.Context, r Request, dir string) ([]string, error) {
	return f(ctx, r, dir)
}

func TestGalleryValidation(t *testing.T) {
	for _, kind := range []string{"empty", "duplicate", "symlink", "too-many", "oversize", "valid"} {
		t.Run(kind, func(t *testing.T) {
			manager, err := New(t.TempDir(), filesRunner(func(_ context.Context, _ Request, dir string) ([]string, error) {
				path := filepath.Join(dir, "001.jpg")
				if err := os.WriteFile(path, []byte("photo"), 0600); err != nil {
					return nil, err
				}
				switch kind {
				case "empty":
					return nil, nil
				case "duplicate":
					return []string{path, path}, nil
				case "symlink":
					link := filepath.Join(dir, "link.jpg")
					return []string{link}, os.Symlink(path, link)
				case "too-many":
					return make([]string, maxJobFiles+1), nil
				case "oversize":
					return []string{path}, os.Truncate(path, maxJobBytes+1)
				}
				return []string{path}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			job, _ := manager.Submit(Request{})
			want := StatusFailed
			if kind == "valid" {
				want = StatusCompleted
			}
			result := awaitStatus(t, manager, job.ID, want)
			if kind == "valid" {
				result.Files[0] = "modified"
				fresh, _ := manager.Get(job.ID)
				if fresh.Files[0] == "modified" {
					t.Fatal("snapshot aliases manager files")
				}
			}
		})
	}
}
