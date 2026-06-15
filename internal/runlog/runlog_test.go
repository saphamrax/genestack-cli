package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerWritesStepAndRunLogs(t *testing.T) {
	base := t.TempDir()
	l, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	if l.Dir() == "" || filepath.Dir(l.Dir()) != base {
		t.Fatalf("unexpected dir %q", l.Dir())
	}

	l.Event("run start")
	w, closeStep := l.Step("k8s.cluster")
	w("line one")
	w("line two")
	closeStep()
	l.Event("✓ k8s.cluster")
	l.Close()

	// Per-step file exists with both lines.
	stepLog := filepath.Join(l.Dir(), "k8s.cluster.log")
	b, err := os.ReadFile(stepLog)
	if err != nil {
		t.Fatalf("step log: %v", err)
	}
	if !strings.Contains(string(b), "line one") || !strings.Contains(string(b), "line two") {
		t.Errorf("step log missing lines:\n%s", b)
	}

	// run.log has the event and the prefixed step lines.
	rb, err := os.ReadFile(filepath.Join(l.Dir(), "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	run := string(rb)
	for _, want := range []string{"run start", "[k8s.cluster] line one", "✓ k8s.cluster"} {
		if !strings.Contains(run, want) {
			t.Errorf("run.log missing %q:\n%s", want, run)
		}
	}
}

func TestNilLoggerIsNoop(t *testing.T) {
	var l *Logger // nil
	l.Event("nothing %d", 1)
	w, c := l.Step("x")
	w("ignored")
	c()
	l.Close()
	if l.Dir() != "" {
		t.Error("nil logger Dir should be empty")
	}
}
