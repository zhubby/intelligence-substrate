package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func TestFeedbackPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	app, e := Open(path, "feedback", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	request := httptest.NewRequest("POST", "/api/feedback", bytes.NewBufferString(`{"title":"Feature","content":"Please support export"}`))
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	restored, e := Open(path, "feedback", "", nil)
	if e != nil || len(restored.data.Feedback) != 1 {
		t.Fatal("stored feedback lost on restart")
	}
}
func TestConcurrentBookingHasOneWinnerAndOpaqueOwnership(t *testing.T) {
	app, e := Open(filepath.Join(t.TempDir(), "state.json"), "booking", "", []Slot{{ID: "slot", StartsAt: "2027-01-01T09:00:00Z"}})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := httptest.NewRecorder()
			app.ServeHTTP(response, httptest.NewRequest("POST", "/api/bookings", bytes.NewBufferString(`{"slotId":"slot"}`)))
			responses <- response
		}()
	}
	wg.Wait()
	close(responses)
	winners, conflicts := 0, 0
	token := ""
	for response := range responses {
		if response.Code == 201 {
			winners++
			var out map[string]string
			_ = json.Unmarshal(response.Body.Bytes(), &out)
			token = out["bookingToken"]
		} else if response.Code == 409 {
			conflicts++
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal("double booking succeeded")
	}
	for _, value := range app.data.Bookings {
		if value == token {
			t.Fatal("plaintext cancellation token stored")
		}
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest("POST", "/api/bookings/cancel", bytes.NewBufferString(`{"bookingToken":"wrong"}`)))
	if response.Code != 404 {
		t.Fatal("another user cancelled booking")
	}
	response = httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"bookingToken": token})
	app.ServeHTTP(response, httptest.NewRequest("POST", "/api/bookings/cancel", bytes.NewReader(body)))
	if response.Code != 200 || app.data.Slots[0].Reserved {
		t.Fatal("owner could not cancel booking")
	}
}
