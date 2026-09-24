package netinventory

// Change is a local netlink notification, never an interface identity. Loss of
// the event stream requires revoking all dependent leases until a fresh sample.
type Change struct {
	Indices map[int]bool
	Lost    bool
}

func publishChange(out chan Change, change Change) {
	select {
	case out <- change:
		return
	default:
	}
	// Coalesce bounded bursts instead of silently dropping deletion/address
	// notifications. Overflow still reports lost continuity explicitly.
	select {
	case old := <-out:
		change.Lost = change.Lost || old.Lost
		if change.Indices == nil {
			change.Indices = map[int]bool{}
		}
		for index := range old.Indices {
			change.Indices[index] = true
		}
	default:
	}
	if len(change.Indices) > 256 {
		change.Lost, change.Indices = true, nil
	}
	out <- change // one producer; a consumer can only make more room
}
