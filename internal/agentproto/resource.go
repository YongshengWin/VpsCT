package agentproto

import "fmt"

// ResourceIdentity keeps ordinary node IDs byte-for-byte compatible while
// giving forwarding its own accounting namespace. Never encode a forward as a
// synthetic node: it must not inherit share accounting or subscription output.
type ResourceIdentity struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

const ForwardMarkPrefix uint32 = 0x45000000

func (r ResourceIdentity) Mark() (uint32, error) {
	if r.ID <= 0 || r.ID > 0xffffff {
		return 0, fmt.Errorf("%s ID outside accounting mark range: %d", r.Kind, r.ID)
	}
	switch r.Kind {
	case "node":
		return NodeMarkPrefix | uint32(r.ID), nil
	case "forward":
		return ForwardMarkPrefix | uint32(r.ID), nil
	default:
		return 0, fmt.Errorf("unsupported managed resource kind: %q", r.Kind)
	}
}

func (r ResourceIdentity) Tag() (string, error) {
	if _, err := r.Mark(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d", r.Kind, r.ID), nil
}
