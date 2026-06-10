package deployer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nogo/herald/internal/ui"
)

// TestComposeFilterWriterNilWriter reproduces the daemon-deploy crash: in daemon
// context the UI is ui.Nop(), whose StreamWriter() is nil. composeFilterWriter
// must discard rather than dereference it (previously a SIGSEGV at deploy time).
func TestComposeFilterWriterNilWriter(t *testing.T) {
	if ui.Nop().StreamWriter() != nil {
		t.Fatal("precondition: ui.Nop().StreamWriter() must be nil")
	}
	f := &composeFilterWriter{w: ui.Nop().StreamWriter()} // w is nil
	n, err := f.Write([]byte("Container herald-nextcloud-app-1  Started\nWaiting\n"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n == 0 {
		t.Fatal("Write reported 0 bytes consumed")
	}
	f.Flush() // must not panic
}

func TestComposeFilterWriterFiltersAndForwards(t *testing.T) {
	var buf bytes.Buffer
	f := &composeFilterWriter{w: &buf}
	f.Write([]byte("0123abcd Pulling fs layer\nContainer moria  Started\n"))
	f.Flush()

	out := buf.String()
	if strings.Contains(out, "Pulling fs layer") {
		t.Errorf("layer pull chatter was not filtered:\n%s", out)
	}
	if !strings.Contains(out, "Container moria  Started") {
		t.Errorf("container line should pass through:\n%s", out)
	}
}
