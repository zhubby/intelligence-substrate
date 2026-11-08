package runtimehost

import (
	"context"
	"fmt"
)

// Reconcile is an explicit administrator decision on the trusted node. It never
// reimports an image or reexecutes publication input from an uncertain request.
func (e *Engine) Reconcile(ctx context.Context, key, keep string) (Operation, error) {
	op, err := e.Store.Inspect(ctx, key)
	if err != nil {
		return op, err
	}
	if op.State == "complete" || op.State == "failed" {
		return op, fmt.Errorf("operation already has a terminal receipt")
	}
	if keep != "previous" && keep != "candidate" {
		return op, fmt.Errorf("explicit previous/candidate decision required")
	}
	current, _ := e.Store.Active(ctx)
	if current.ID != op.Previous && current.ID != op.Intent.ID {
		return op, fmt.Errorf("another activation is current; reconcile all external state before changing it")
	}
	if op.State == "reserved" {
		if keep != "previous" {
			return op, fmt.Errorf("no candidate switch was recorded")
		}
		err = e.Store.finish(ctx, key, Result{}, "administrator confirmed no application switch occurred")
		if err != nil {
			return op, err
		}
		return e.Store.Inspect(ctx, key)
	}
	var chosen Deployment
	if keep == "candidate" {
		if op.Previous != "" {
			previous, err := e.Store.Lookup(ctx, op.Previous)
			if err != nil {
				return op, err
			}
			if err = e.Driver.Stop(ctx, previous); err != nil {
				return op, err
			}
		}
		chosen, err = e.Driver.Start(ctx, op.Intent)
	} else {
		if err = e.Driver.Stop(ctx, op.Intent); err != nil {
			return op, err
		}
		if op.Previous == "" {
			return op, fmt.Errorf("no previous deployment exists; inspect the stopped candidate before abandoning this operation")
		}
		previous, err := e.Store.Lookup(ctx, op.Previous)
		if err != nil {
			return op, err
		}
		chosen, err = e.Driver.Start(ctx, previous)
	}
	if err != nil {
		return op, err
	}
	if err = e.waitReady(ctx, chosen); err != nil {
		return op, err
	}
	if err = e.Store.activate(ctx, chosen, current.ID); err != nil {
		return op, err
	}
	result := e.result(chosen)
	message := ""
	if keep == "previous" {
		message = "administrator restored and checked the previous deployment"
	}
	if err = e.Store.finish(ctx, key, result, message); err != nil {
		return op, err
	}
	return e.Store.Inspect(ctx, key)
}
