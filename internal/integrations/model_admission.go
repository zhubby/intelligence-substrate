package integrations

import (
	"errors"
	"fmt"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

type modelAdmissionWait struct{ until time.Time }

func (e *modelAdmissionWait) Error() string { return "waiting for configured provider admission" }

// Admission and the charge reservation commit in one transaction. Waiting holds
// neither a SQL transaction nor a charge. The durable gate coordinates replicas
// and survives a control-plane restart; it never replays a failed external call.
func (m *Model) admitModel(tx *p.Tx) error {
	if m.MinInterval <= 0 {
		return nil
	}
	if m.MinInterval > 300*time.Second {
		return fmt.Errorf("invalid model admission interval")
	}
	id := "model_gate_" + p.Hash(p.M{"origin": m.BaseURL, "model": m.Name})
	gate, err := tx.Get(id)
	if err != nil {
		var fault *p.Fault
		if !errors.As(err, &fault) || fault.Code != "not_found" {
			return err
		}
		gate = p.Resource{ID: id, Kind: "model_admission", Status: "active", Revision: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Data: p.M{}}
	} else {
		next, err := time.Parse(time.RFC3339Nano, p.S(gate.Data, "nextAt"))
		if err != nil {
			return fmt.Errorf("stored provider admission needs administrator reconciliation")
		}
		if time.Now().Before(next) {
			return &modelAdmissionWait{until: next}
		}
	}
	gate.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	gate.Data["nextAt"] = time.Now().Add(m.MinInterval).UTC().Format(time.RFC3339Nano)
	return tx.Put(gate)
}
