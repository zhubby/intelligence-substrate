package integrations

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAXCleanupCannotLeaveASuccessfulCommand(t *testing.T) {
	var archive bytes.Buffer
	if e := tar.NewWriter(&archive).Close(); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"delete-denied", "retained", "read-denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			ax, log := axScript(t, fmt.Sprintf(`case "$1" in
resume) printf '%%s' "$3" > "$(dirname "$0")/name" ;;
get)
  case "$2" in
  task) printf 'status:\n  phase: Running\n' ;;
  tasks)
    case %s in
    read-denied) echo 'listing denied' >&2; exit 1 ;;
    malformed) printf '' ;;
    retained) printf 'NAME ATESPACE PHASE ACTOR WORKER-IP AGE\n'; printf '%%s test Terminating actor host 1s\n' "$(cat "$(dirname "$0")/name")" ;;
    *) printf 'NAME ATESPACE PHASE ACTOR WORKER-IP AGE\n' ;;
    esac ;;
  workspaces) printf 'NAME ATESPACE GIT-REPOS MCP-SERVERS\n' ;;
  esac ;;
ssh)
  for last; do :; done
  case "$last" in *'base64 < /workspace/result.tar'*) printf '%%s' %s ;; esac ;;
delete)
  if [ %s = delete-denied ] && [ "$2" = task ]; then echo 'deletion denied' >&2; exit 1; fi ;;
esac
`, shellQuote(mode), shellQuote(base64.StdEncoding.EncodeToString(archive.Bytes())), shellQuote(mode)))
			dir := filepath.Join(t.TempDir(), "source")
			if e := os.Mkdir(dir, 0700); e != nil {
				t.Fatal(e)
			}
			var out bytes.Buffer
			exit, e := ax.RunCommand(context.Background(), dir, []string{"/bin/true"}, &out)
			if e == nil || exit == 0 {
				t.Fatal("unconfirmed remote cleanup was reported as successful", exit, e)
			}
			commands, e := os.ReadFile(log)
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(string(commands), "delete task") || !strings.Contains(string(commands), "delete workspace") {
				t.Fatal("both owned resources were not cleaned", string(commands))
			}
		})
	}
}
