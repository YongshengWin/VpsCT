package conntail

import (
	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentproto"
)

// NewSharedQueue gives both readers one fixed ring: payload <=3 MiB, slot
// storage <1 MiB. It is retained and reused instead of repeatedly growing.
func NewSharedQueue() *Tailer {
	return &Tailer{MaxEvents: agentbudget.QueueSlots, MaxBufferBytes: agentbudget.QueuePayloadBytes, fixed: true, buf: make([]agentproto.ConnEvent, agentbudget.QueueSlots)}
}
func (t *Tailer) clearQueue() {
	if t.Queue != nil {
		t.Queue.clearQueue()
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.buf)
	t.head = 0
	t.count = 0
	t.bufferedBytes = 0
	if !t.fixed {
		t.buf = nil
	}
}
