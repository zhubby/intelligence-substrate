package previewnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Driver interface {
	Start(context.Context, Binding, []byte) error
	Request(context.Context, Binding, Request) (Response, error)
	Stop(context.Context, Binding) error
}
type Record struct {
	Binding    Binding   `json:"binding"`
	State      string    `json:"state"`
	LeaseUntil time.Time `json:"leaseUntil"`
}
type Engine struct {
	mu      sync.Mutex
	records map[string]Record
	dir     string
	driver  Driver
	limit   int
}
type journal struct {
	Version int               `json:"version"`
	Records map[string]Record `json:"records"`
}

func Open(dir string, driver Driver, limit int) (*Engine, error) {
	if !filepath.IsAbs(dir) || driver == nil || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("private absolute state directory, driver and capacity 1–100 required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("preview node state directory must be private 0700 and not linked")
	}
	e := &Engine{dir: dir, driver: driver, limit: limit, records: map[string]Record{}}
	body, err := os.ReadFile(filepath.Join(dir, "instances.json"))
	if os.IsNotExist(err) {
		return e, nil
	}
	if err != nil {
		return nil, err
	}
	var j journal
	if json.Unmarshal(body, &j) != nil || j.Version != 1 || j.Records == nil || len(j.Records) > 10000 {
		return nil, conflict()
	}
	for key, r := range j.Records {
		if key != r.Binding.PreviewID || !r.Binding.valid() || r.LeaseUntil.IsZero() {
			return nil, conflict()
		}
		switch r.State {
		case "running", "stopped", "uncertain":
		case "starting", "stopping":
			r.State = "uncertain"
		default:
			return nil, conflict()
		}
		e.records[key] = r
	}
	return e, nil
}

// save is called while locked, before every external lifecycle side effect.
func (e *Engine) save() error {
	body, err := json.MarshalIndent(journal{1, e.records}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(e.dir, ".instances-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(e.dir, "instances.json")); err != nil {
		return err
	}
	dir, err := os.Open(e.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (e *Engine) set(r Record) error { e.records[r.Binding.PreviewID] = r; return e.save() }
func lease(b Binding) time.Time {
	deadline := time.Now().UTC().Add(time.Minute)
	if b.ExpiresAt.Before(deadline) {
		deadline = b.ExpiresAt
	}
	return deadline
}
func (e *Engine) Ensure(ctx context.Context, b Binding, binary []byte) (Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !b.live() {
		return Record{}, conflict()
	}
	if r, ok := e.records[b.PreviewID]; ok {
		if !r.Binding.matches(b) || r.State != "running" || !r.LeaseUntil.After(time.Now()) {
			return r, conflict()
		}
		r.LeaseUntil = lease(b)
		return r, e.set(r)
	}
	if !b.verify(binary) {
		return Record{}, fmt.Errorf("预览后端内容不匹配")
	}
	active := 0
	for _, r := range e.records {
		if r.State != "stopped" {
			active++
		}
	}
	if active >= e.limit || len(e.records) >= 10000 {
		return Record{}, fmt.Errorf("预览节点容量已满")
	}
	r := Record{Binding: b, State: "starting", LeaseUntil: lease(b)}
	if err := e.set(r); err != nil {
		return r, err
	}
	if err := e.driver.Start(ctx, b, binary); err != nil {
		r.State = "uncertain"
		_ = e.set(r)
		return r, conflict()
	}
	r.State = "running"
	if err := e.set(r); err != nil {
		return r, conflict()
	}
	return r, nil
}
func (e *Engine) Request(ctx context.Context, b Binding, q Request) (Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.records[b.PreviewID]
	if !ok || !r.Binding.matches(b) || r.State != "running" || !b.live() || !r.LeaseUntil.After(time.Now()) || !q.Valid(b.Runtime.Mode) {
		return Response{}, conflict()
	}
	// A lost write response is never replayed, including after service restart.
	result, err := e.driver.Request(ctx, b, q)
	if err != nil || !result.Valid() {
		return Response{}, fmt.Errorf("业务请求结果不明，请刷新核对后再操作")
	}
	return result, nil
}
func (e *Engine) Renew(ctx context.Context, b Binding) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.records[b.PreviewID]
	if !ok || !r.Binding.matches(b) || r.State != "running" || !b.live() || !r.LeaseUntil.After(time.Now()) {
		return conflict()
	}
	r.LeaseUntil = lease(b)
	return e.set(r)
}
func (e *Engine) stop(ctx context.Context, r Record) error {
	if r.State == "stopped" {
		return nil
	}
	r.State = "stopping"
	if err := e.set(r); err != nil {
		return err
	}
	if err := e.driver.Stop(ctx, r.Binding); err != nil {
		r.State = "uncertain"
		_ = e.set(r)
		return conflict()
	}
	r.State = "stopped"
	return e.set(r)
}
func (e *Engine) Stop(ctx context.Context, b Binding) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.records[b.PreviewID]
	if !ok {
		return nil
	}
	if !r.Binding.matches(b) {
		return conflict()
	}
	return e.stop(ctx, r)
}
func (e *Engine) List() []Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Record{}
	for _, r := range e.records {
		if r.State != "stopped" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Binding.PreviewID < out[j].Binding.PreviewID })
	return out
}
func (e *Engine) Reap(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var failures []error
	for _, r := range e.records {
		if r.State != "stopped" && (!r.Binding.ExpiresAt.After(time.Now()) || !r.LeaseUntil.After(time.Now())) {
			if err := e.stop(ctx, r); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
