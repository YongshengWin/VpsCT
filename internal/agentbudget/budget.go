// Package agentbudget is the single resource envelope for each agent process.
// Payload limits reserve additional room for Go structures and runtime pages.
package agentbudget

const (
	ActiveNodes        = 256
	QueueSlots         = 8192
	QueuePayloadBytes  = 3 << 20 // plus <1 MiB fixed ring storage
	PairEntriesPerTail = 256     // both pairing maps, two tails: <2 MiB total
	ResidentGoBytes    = 64 << 20
	WorkerGoLimit      = "48MiB"
	NetworkGoLimit     = "32MiB"
	ConfigBytes        = 2 << 20
	SettlementBytes    = 1 << 20
)
