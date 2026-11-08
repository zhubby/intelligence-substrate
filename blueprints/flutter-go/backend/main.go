package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Feedback struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}
type Slot struct {
	ID       string `json:"id"`
	StartsAt string `json:"startsAt"`
	Reserved bool   `json:"reserved"`
}
type state struct {
	Feedback []Feedback        `json:"feedback"`
	Slots    []Slot            `json:"slots"`
	Bookings map[string]string `json:"bookings"`
}
type App struct {
	mu                 sync.Mutex
	Path, Mode, Origin string
	data               state
}

func Open(path, mode, origin string, slots []Slot) (*App, error) {
	if mode != "feedback" && mode != "booking" {
		return nil, errors.New("APP_MODE must be feedback or booking")
	}
	a := &App{Path: path, Mode: mode, Origin: origin, data: state{Feedback: []Feedback{}, Slots: slots, Bookings: map[string]string{}}}
	b, e := os.ReadFile(path)
	if e == nil {
		e = json.Unmarshal(b, &a.data)
	} else if os.IsNotExist(e) {
		e = nil
	}
	if a.data.Bookings == nil {
		a.data.Bookings = map[string]string{}
	}
	return a, e
}
func (a *App) save(next state) error {
	if e := os.MkdirAll(filepath.Dir(a.Path), 0700); e != nil {
		return e
	}
	file, e := os.CreateTemp(filepath.Dir(a.Path), "state-")
	if e != nil {
		return e
	}
	defer os.Remove(file.Name())
	if e = json.NewEncoder(file).Encode(next); e != nil {
		file.Close()
		return e
	}
	if e = file.Sync(); e != nil {
		file.Close()
		return e
	}
	if e = file.Close(); e != nil {
		return e
	}
	if e = os.Rename(file.Name(), a.Path); e != nil {
		return e
	}
	a.data = next
	return nil
}
func (a *App) copy() state {
	b, _ := json.Marshal(a.data)
	var next state
	_ = json.Unmarshal(b, &next)
	return next
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func identifier() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if origin := r.Header.Get("Origin"); origin != "" {
		if origin != a.Origin {
			respond(w, 403, map[string]string{"error": "Untrusted web origin"})
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/api/health" && r.Method == "GET" {
		respond(w, 200, map[string]string{"status": "ok", "mode": a.Mode})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Mode == "feedback" && r.URL.Path == "/api/feedback" && r.Method == "GET" {
		respond(w, 200, a.data.Feedback)
		return
	}
	if a.Mode == "booking" && r.URL.Path == "/api/slots" && r.Method == "GET" {
		respond(w, 200, a.data.Slots)
		return
	}
	if r.Method != "POST" {
		respond(w, 404, map[string]string{"error": "Not found"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var in map[string]string
	d := json.NewDecoder(r.Body)
	if e := d.Decode(&in); e != nil {
		respond(w, 400, map[string]string{"error": "Invalid JSON"})
		return
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		respond(w, 400, map[string]string{"error": "Unexpected trailing data"})
		return
	}
	next := a.copy()
	if a.Mode == "feedback" && r.URL.Path == "/api/feedback" {
		title, content := strings.TrimSpace(in["title"]), strings.TrimSpace(in["content"])
		if title == "" || len(title) > 200 || len(content) > 8000 {
			respond(w, 422, map[string]string{"error": "Title is required (max 200 bytes); content max 8000 bytes"})
			return
		}
		if len(next.Feedback) >= 1000 {
			respond(w, 503, map[string]string{"error": "Feedback capacity reached"})
			return
		}
		item := Feedback{ID: identifier(), Title: title, Content: content, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
		next.Feedback = append([]Feedback{item}, next.Feedback...)
		if e := a.save(next); e != nil {
			respond(w, 500, map[string]string{"error": "Could not persist feedback"})
			return
		}
		respond(w, 201, item)
		return
	}
	if a.Mode == "booking" && r.URL.Path == "/api/bookings" {
		for i, slot := range next.Slots {
			if slot.ID != in["slotId"] {
				continue
			}
			if slot.Reserved {
				respond(w, 409, map[string]string{"error": "This slot is already reserved"})
				return
			}
			token := identifier()
			next.Slots[i].Reserved = true
			next.Bookings[digest(token)] = slot.ID
			if e := a.save(next); e != nil {
				respond(w, 500, map[string]string{"error": "Could not persist booking"})
				return
			}
			respond(w, 201, map[string]string{"bookingToken": token, "slotId": slot.ID})
			return
		}
		respond(w, 404, map[string]string{"error": "Slot not found"})
		return
	}
	if a.Mode == "booking" && r.URL.Path == "/api/bookings/cancel" {
		hash := digest(in["bookingToken"])
		id, ok := next.Bookings[hash]
		if !ok {
			respond(w, 404, map[string]string{"error": "Booking not found"})
			return
		}
		delete(next.Bookings, hash)
		for i := range next.Slots {
			if next.Slots[i].ID == id {
				next.Slots[i].Reserved = false
			}
		}
		if e := a.save(next); e != nil {
			respond(w, 500, map[string]string{"error": "Could not persist cancellation"})
			return
		}
		respond(w, 200, map[string]string{"status": "cancelled"})
		return
	}
	respond(w, 404, map[string]string{"error": "Not found"})
}
func main() {
	path := os.Getenv("APP_STATE_FILE")
	if path == "" {
		path = "data/state.json"
	}
	mode := os.Getenv("APP_MODE")
	if mode == "" {
		mode = "feedback"
	}
	slots := []Slot{}
	if raw := os.Getenv("APP_SLOTS"); raw != "" {
		if e := json.Unmarshal([]byte(raw), &slots); e != nil {
			log.Fatal(e)
		}
	}
	app, e := Open(path, mode, os.Getenv("APP_WEB_ORIGIN"), slots)
	if e != nil {
		log.Fatal(e)
	}
	addr := os.Getenv("APP_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8081"
	}
	server := &http.Server{Addr: addr, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
