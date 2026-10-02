package download

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"unicode"
)

var diagnosticURL = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
var diagnosticSecret = regexp.MustCompile(`(?i)(authorization|cookie|set-cookie|api[-_]?key|x-job-token|token|password|signature|sig)\s*[:=]\s*[^\r\n]+`)

// Keep diagnostics in server logs, bounded and without URLs or credentials.
func diagnostic(text string) string {
	text = diagnosticURL.ReplaceAllString(text, "[url]")
	text = diagnosticSecret.ReplaceAllString(text, "$1=[redacted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	if len(text) > 2048 {
		text = text[:2048]
	}
	return strings.TrimSpace(text)
}

func runMedia(cmd *exec.Cmd) error {
	var stderr boundedOutput
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", cmd.Path[strings.LastIndex(cmd.Path, "/")+1:], err, diagnostic(string(stderr.data)))
	}
	return nil
}
