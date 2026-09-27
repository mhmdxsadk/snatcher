package download

import (
	"context"
	"strings"
	"testing"
)

func TestSanitizedDiagnostics(t *testing.T) {
	raw := "ERROR fetching https://user:secret@example.com/media?sig=SECRET\nAuthorization: Bearer SECRET\nCookie: session=SECRET\ninvalid codec\x1b"
	got := diagnostic(raw)
	if strings.Contains(got, "SECRET") || strings.Contains(got, "secret") || strings.Contains(got, "\x1b") || !strings.Contains(got, "invalid codec") {
		t.Fatal(got)
	}
	if len(diagnostic(strings.Repeat("x", 10000))) > 2048 {
		t.Fatal("unbounded diagnostic")
	}
	err := runMedia(mediaCommand(context.Background(), "sh", "-c", "echo 'decoder failure' >&2; exit 1"))
	if err == nil || !strings.Contains(err.Error(), "decoder failure") {
		t.Fatal(err)
	}
}
