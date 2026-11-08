package publisher

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGoogleQueryGuardStatusOwnsWindowAcrossTwoEngines(t *testing.T) {
	for _, scenario := range []string{"publish", "another track", "another status", "another app"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, r, e, upstream := googleWriteFixture(t)
			result, err := e.Execute(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			p := cfg.Profiles[0]
			p.GoalID, p.AppID = "goal_two", "com.example.booking"
			cfg.Profiles = append(cfg.Profiles, p)
			other, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			other.apiBase, other.HTTP, other.tokenSource = e.apiBase, e.HTTP, e.tokenSource
			ready, resume := make(chan struct{}), make(chan struct{})
			var resumed sync.Once
			defer resumed.Do(func() { close(resume) })
			var waiting atomic.Bool
			upstream.mu.Lock()
			before := len(upstream.requests)
			upstream.hook = func(q *http.Request) {
				if q.Method == "POST" && strings.HasSuffix(q.URL.Path, "/edits") && waiting.CompareAndSwap(false, true) {
					close(ready)
					<-resume
				}
			}
			upstream.mu.Unlock()
			query := r
			query.Action, query.ExternalID, query.ArtifactPath = "status", result.ExternalID, ""
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			observed := make(chan error, 1)
			go func() { _, err := e.Execute(ctx, query); observed <- err }()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("query never entered the provider edit creation")
			}
			changed := nextGoogleRequest(t, r)
			if scenario == "another app" {
				changed.GoalID = "goal_two"
			}
			if scenario == "another track" {
				other.Config.Profiles[0].Track = "internal"
			}
			if scenario == "another status" {
				changed = query
			}
			_, attemptErr := other.Execute(ctx, changed)
			allowed := scenario == "another app"
			if (attemptErr == nil) != allowed {
				t.Error("query and publishing did not share the application edit window", attemptErr)
			}
			upstream.mu.Lock()
			if !allowed && len(upstream.requests) != before+1 {
				t.Error("a competing operation reached Google while the first query was live", fmt.Sprint(upstream.requests))
			}
			upstream.mu.Unlock()
			resumed.Do(func() { close(resume) })
			if err = <-observed; err != nil {
				t.Fatal("original query could not finish", err)
			}
		})
	}
}
