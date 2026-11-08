package integrations

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func axScript(t *testing.T, body string) (AX, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell CLI fixture requires a POSIX host")
	}
	root := t.TempDir()
	log := filepath.Join(root, "commands")
	script := filepath.Join(root, "ax")
	text := "#!/bin/sh\nwhile [ \"${1#--}\" != \"$1\" ]; do shift; done\nprintf '%s %s\\n' \"$1\" \"$2\" >> " + shellQuote(log) + "\n" + body
	if err := os.WriteFile(script, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	return AX{Binary: script, AllowLegacyCLI: true, Namespace: "ax-system", Atespace: "test", Image: "runner@sha256:" + strings.Repeat("a", 64)}, log
}

func TestAXFailedTaskStopsBeforeGuestExecution(t *testing.T) {
	ax, log := axScript(t, `case "$1" in
get) cat <<'YAML'
apiVersion: ax.io/v1alpha1
kind: Task
status:
  phase: Failed
  conditions:
    - type: Ready
      status: "False"
      reason: ActorResumeFailed
      message: no worker has room for the actor
YAML
;;
ssh) exit 1;;
esac
`)
	dir := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// The assertion is terminal-state handling, not host process launch speed.
	// Leave room for concurrent race-instrumented integration tests on CI.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	exit, err := ax.RunCommand(ctx, dir, []string{"/bin/true"}, &output)
	if err == nil || exit == 0 || !strings.Contains(err.Error(), "ActorResumeFailed") || !strings.Contains(err.Error(), "no worker has room") {
		t.Fatalf("terminal failure was not reported: exit=%d err=%v", exit, err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "ssh ") {
		t.Fatal("failed task reached guest execution")
	}
}

func TestAXCancelAttemptsWorkspaceAfterTaskFailure(t *testing.T) {
	ax, log := axScript(t, `if [ "$1" = delete ] && [ "$2" = task ]; then
  echo 'Error: deletion blocked by active operation' >&2
  exit 1
fi
`)
	err := ax.Cancel(context.Background(), "owned-task")
	if err == nil || !strings.Contains(err.Error(), "deletion blocked by active operation") {
		t.Fatalf("cleanup lost diagnostic: %v", err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "delete workspace") {
		t.Fatal("workspace cleanup skipped after task failure")
	}
}

func TestAXRequestFailureIncludesBoundedDiagnostic(t *testing.T) {
	ax, _ := axScript(t, "printf 'Error: unsupported Control API\\n' >&2\nexit 1\n")
	_, err := ax.Describe(context.Background(), "owned-task")
	if err == nil || !strings.Contains(err.Error(), "unsupported Control API") {
		t.Fatalf("AX diagnostic missing: %v", err)
	}
}
