package platform

import (
	"context"
	"fmt"
	"time"
)

const runtimeSignalTTL = 90 * time.Second

// ReconcileRuntimeSignals runs in the control plane, independently of workers.
// Store.Update serializes competing processes and commits the event with audit.
func (s *Service) ReconcileRuntimeSignals(ctx context.Context) error {
	return s.Store.Update(ctx, func(tx *Tx) error {
		return reconcileRuntimeSignals(tx, time.Now())
	})
}

func reconcileRuntimeSignals(t *Tx, at time.Time) error {
	deployments, err := t.List("", "deployment", "")
	if err != nil {
		return err
	}
	for _, d := range deployments {
		if d.Status != "active" {
			continue
		}
		if err = detectRuntimeSilence(t, d, at); err != nil {
			return err
		}
	}
	return nil
}

func runtimeSignalDeadline(d Resource) (time.Time, error) {
	if value := S(d.Data, "validUntil"); value != "" {
		return time.Parse(time.RFC3339Nano, value)
	}
	activation := S(d.Data, "activatedAt")
	if activation == "" {
		activation = d.CreatedAt
	}
	at, err := time.Parse(time.RFC3339Nano, activation)
	return at.Add(runtimeSignalTTL), err
}

func detectRuntimeSilence(t *Tx, d Resource, at time.Time) error {
	deadline, err := runtimeSignalDeadline(d)
	if err != nil {
		return fmt.Errorf("deployment %s has an invalid signal deadline", d.ID)
	}
	if at.Before(deadline) {
		return nil
	}
	_, err = openRuntimeIncident(t, d, "monitor_signal_lost", at, M{"signalDeadline": deadline.UTC().Format(time.RFC3339Nano), "lastCheckedAt": S(d.Data, "lastCheckedAt")})
	return err
}

func runtimeIncidentType(r Resource) string {
	if kind := S(r.Data, "type"); kind != "" {
		return kind
	}
	// Existing incidents predate the type field and represented failed probes.
	return "application_unhealthy"
}

func openRuntimeIncidents(t *Tx, d Resource, kind string) ([]Resource, error) {
	all, err := t.List(d.ProjectID, "incident", d.GoalID)
	if err != nil {
		return nil, err
	}
	out := []Resource{}
	for _, r := range all {
		if r.Status == "open" && runtimeIncidentType(r) == kind && S(r.Data, "application") == S(d.Data, "application") && S(r.Data, "workerId") == S(d.Data, "workerId") {
			out = append(out, r)
		}
	}
	return out, nil
}

func runtimeIncidentAudit(t *Tx, action string, r Resource) error {
	return t.Audit(Actor{ID: "system:runtime-monitor", Kind: "system"}, action, r, M{"type": runtimeIncidentType(r), "status": r.Status, "deploymentId": S(r.Data, "deploymentId"), "recoveredDeploymentId": S(r.Data, "recoveredDeploymentId"), "recoverySequence": N(r.Data, "recoverySequence")})
}

func openRuntimeIncident(t *Tx, d Resource, kind string, at time.Time, details M) (Resource, error) {
	open, err := openRuntimeIncidents(t, d, kind)
	if err != nil {
		return Resource{}, err
	}
	if len(open) != 0 {
		return open[0], nil
	}
	title := "应用连续健康检查失败"
	if kind == "monitor_signal_lost" {
		title = "运行监测信号中断"
	}
	data := M{"type": kind, "deploymentId": d.ID, "externalId": S(d.Data, "externalId"), "application": S(d.Data, "application"), "workerId": S(d.Data, "workerId"), "releaseId": S(d.Data, "releaseId"), "commit": S(d.Data, "commit"), "artifactDigest": S(d.Data, "artifactDigest"), "triggerSequence": N(d.Data, "lastSequence"), "openedAt": at.UTC().Format(time.RFC3339Nano)}
	for key, value := range details {
		data[key] = value
	}
	r := resource("incident", title, d.ProjectID, d.GoalID, data)
	r.Status = "open"
	if err = t.Put(r); err != nil {
		return r, err
	}
	return r, runtimeIncidentAudit(t, "runtime.incident.opened", r)
}

func resolveRuntimeIncidents(t *Tx, d Resource, kind, reason string, at time.Time) error {
	open, err := openRuntimeIncidents(t, d, kind)
	if err != nil {
		return err
	}
	for _, r := range open {
		r.Status = "resolved"
		r.Data["resolvedAt"] = at.UTC().Format(time.RFC3339Nano)
		r.Data["resolutionReason"] = reason
		r.Data["recoveredDeploymentId"] = d.ID
		r.Data["recoverySequence"] = N(d.Data, "lastSequence")
		r.Data["recoveryHealth"] = S(d.Data, "health")
		if err = t.Put(r); err != nil {
			return err
		}
		if err = runtimeIncidentAudit(t, "runtime.incident.resolved", r); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) acknowledgeRuntimeIncident(t *Tx, a Actor, in M) (Resource, error) {
	incident, err := s.scoped(t, a, S(in, "id"), "incident", "release", "quality")
	if err != nil {
		return incident, err
	}
	if err = human(a); err != nil {
		return incident, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return incident, err
	}
	if err = validateText(S(in, "note"), "处理说明", 2000); err != nil {
		return incident, err
	}
	scope := "runtime-incident-ack:" + incident.ID + ":" + a.ID
	if previous, ok, err := t.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	if incident.Status != "open" || S(incident.Data, "acknowledgedBy") != "" {
		return incident, conflict("事件已经恢复或已有人接手，请刷新事件状态")
	}
	incident.Data["acknowledgedBy"] = a.ID
	incident.Data["acknowledgedName"] = a.Name
	incident.Data["acknowledgedAt"] = now()
	incident.Data["acknowledgementNote"] = S(in, "note")
	if err = t.Put(incident); err != nil {
		return incident, err
	}
	return incident, t.Remember(scope, S(in, "key"), in, incident)
}
