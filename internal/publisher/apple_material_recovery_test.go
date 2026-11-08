package publisher

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAppleManagedRecoveryVerifiesMaterialsAndReleasesOnlySubmittedLock(t *testing.T) {
	for _, phase := range []string{"upload", "review"} {
		for _, scenario := range []string{"accepted", "text-drift", "image-wait", "lock-lost"} {
			t.Run(phase+"/"+scenario, func(t *testing.T) {
				_, r, e, base, state, _ := appleManagedFixture(t)
				defer e.Close()
				ctx := context.Background()
				path := "/v1/buildUploadFiles/file1"
				if phase == "review" {
					path = "/v1/reviewSubmissions/submission1"
				}
				e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, res *http.Response) (*http.Response, error) {
					if q.Method == "PATCH" && q.URL.Path == path {
						res.Body.Close()
						res.StatusCode = 503
						res.Body = io.NopCloser(strings.NewReader(`{"errors":[]}`))
					}
					return res, nil
				}}
				result, err := e.Execute(ctx, r)
				base.mu.Lock()
				base.uploadState = "COMPLETE"
				base.mu.Unlock()
				state.mu.Lock()
				state.imageReady = true
				state.mu.Unlock()
				if phase == "review" {
					if err != nil {
						t.Fatal(err)
					}
					r.Action = "continue"
					r.ExternalID = result.ExternalID
					_, err = e.Execute(ctx, r)
				}
				if err == nil {
					t.Fatal("fixture failed to lose accepted response")
				}
				op, err := e.Inspect(ctx, r.OperationKey)
				if err != nil {
					t.Fatal(err)
				}
				in := AppleReconciliationRequest{Request: r, JournalDigest: op.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey, Reason: "Operator stopped the exact process and verified outstanding writes ended"}
				state.mu.Lock()
				before := len(state.writes)
				switch scenario {
				case "text-drift":
					state.infoText["privacyPolicyUrl"] = "https://unapproved.example/privacy"
				case "image-wait":
					state.imageReady = false
					state.image["state"] = "UPLOAD_COMPLETE"
				}
				state.mu.Unlock()
				if scenario == "lock-lost" {
					if _, err = e.db.Exec("DELETE FROM apple_destinations"); err != nil {
						t.Fatal(err)
					}
				}
				recovered, err := e.ReconcileApple(ctx, in)
				if scenario != "accepted" {
					if err == nil {
						t.Fatal("recovery ignored material drift, processing or lost reservation")
					}
					unchanged, _ := e.Inspect(ctx, r.OperationKey)
					if unchanged.JournalDigest != op.JournalDigest {
						t.Fatal("failed material recovery changed journal")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if recovered.State != "complete" {
						t.Fatal(recovered)
					}
					var locks int
					if err = e.db.QueryRow("SELECT count(*) FROM apple_destinations").Scan(&locks); err != nil {
						t.Fatal(err)
					}
					want := 1
					if phase == "review" {
						want = 0
					}
					if locks != want {
						t.Fatal("recovery released the wrong app reservation", locks)
					}
				}
				state.mu.Lock()
				defer state.mu.Unlock()
				if len(state.writes) != before {
					t.Fatal("material recovery issued an external write")
				}
			})
		}
	}
}

func TestAppleManagedWriteChecksCurrentJournalAndReservations(t *testing.T) {
	for _, change := range []string{"hash", "state", "lock", "version", "deleted"} {
		t.Run(change, func(t *testing.T) {
			_, r, e, _, state, _ := appleManagedFixture(t)
			defer e.Close()
			state.beforeWrite = func(path string) {
				if path != "PATCH /v1/appStoreVersionLocalizations/locale1" {
					return
				}
				var err error
				switch change {
				case "hash":
					_, err = e.db.Exec("UPDATE operations SET request_hash=? WHERE operation_key=?", strings.Repeat("f", 64), r.OperationKey)
				case "state":
					_, err = e.db.Exec("UPDATE operations SET state='complete' WHERE operation_key=?", r.OperationKey)
				case "lock":
					_, err = e.db.Exec("DELETE FROM apple_destinations")
				case "version":
					_, err = e.db.Exec("DELETE FROM apple_versions")
				case "deleted":
					_, err = e.db.Exec("DELETE FROM operations WHERE operation_key=?", r.OperationKey)
				}
				if err != nil {
					t.Error(err)
				}
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("lost or changed reservation permitted additional material writes")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.writes) != 1 {
				t.Fatal("write continued after journal/reservation changed", state.writes)
			}
		})
	}
}
