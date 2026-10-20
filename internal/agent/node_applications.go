package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
)

func (a *Agent) finishApplications(ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, ready, attempted map[string]bool, admissionFailed, egressFailed bool) error {
	err := a.finishNodeApplications(ds, nodes, ready, attempted)
	if egressFailed {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for name := range attempted {
			if driver := a.Drivers[name]; driver != nil {
				err = errors.Join(err, driver.Stop(ctx))
			}
		}
	} else if admissionFailed {
		// Rolling back a core may restart its previous authorized service.
		// A failed network guard still requires that service to remain stopped.
		err = errors.Join(err, a.stopBindingCores(ds))
	}
	return err
}

func (a *Agent) bindingAffectedCores(ds *agentproto.DesiredState) map[string]bool {
	names := map[string]bool{}
	for _, n := range ds.Nodes {
		if n.Network != nil {
			names[n.Core] = true
		}
	}
	if len(ds.Forwards) > 0 {
		names["singbox"] = true
	}
	if a.bindings != nil && a.bindings.plan != nil {
		for _, b := range a.bindings.plan.Bindings {
			names[b.Core] = true
		}
	}
	return names
}

// Finalization precedes receipts: a rolled-back candidate must never be reported
// as applied. Cores may commit independently after their common protection checks.
func (a *Agent) finishNodeApplications(ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, ready, attempted map[string]bool) error {
	var failures []error
	for name, driver := range a.Drivers {
		// Installation may have a staged recovery point while the previous
		// verified service remains available. Only an actual application or
		// an admission fence may finalize/roll back that core.
		if !attempted[name] {
			continue
		}
		if finalizer, ok := driver.(core.ActivationFinalizer); ok {
			finish := func(success bool) error {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				return finalizer.FinishActivation(ctx, success)
			}
			if err := finish(ready[name]); err != nil {
				ready[name] = false
				failures = append(failures, fmt.Errorf("%s activation finalization: %w", name, err))
				if err := finish(false); err != nil {
					failures = append(failures, fmt.Errorf("%s activation rollback: %w", name, err))
					stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					stopErr := driver.Stop(stopContext)
					cancel()
					if stopErr != nil {
						failures = append(failures, fmt.Errorf("%s stop after uncertain activation rollback: %w", name, stopErr))
					}
				}
			}
		}
	}
	prepared := map[int64]agentproto.NodeSpec{}
	for _, n := range nodes {
		prepared[n.NodeID] = n
	}
	if len(attempted) == 0 {
		return errors.Join(failures...)
	}
	previousSequence := a.State.NodeApplicationSequence
	a.State.NodeApplicationRevision, a.State.NodeApplicationHash = ds.Revision, ds.Hash
	a.State.NodeApplicationSequence++
	a.State.NodeApplications = nil
	for _, n := range ds.Nodes {
		if !attempted[n.Core] {
			continue
		}
		status := "failed"
		if ready[n.Core] {
			status = "applied"
			if actual, ok := prepared[n.NodeID]; !ok || actual.Blocked {
				status = "blocked"
			}
		}
		a.State.NodeApplications = append(a.State.NodeApplications, agentproto.NodeApplication{NodeID: n.NodeID, Core: n.Core, Status: status})
	}
	if err := a.State.Save(a.StateDir); err != nil {
		// Never publish an undurable sequence, or resurrect the previous
		// success after resources have been changed/stopped in this attempt.
		a.State.NodeApplicationRevision, a.State.NodeApplicationHash = 0, ""
		a.State.NodeApplicationSequence = previousSequence
		a.State.NodeApplications = nil
		failures = append(failures, fmt.Errorf("persist resource application receipts: %w", err))
	}
	return errors.Join(failures...)
}
