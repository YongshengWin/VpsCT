// Package agentbudget is the single resource envelope for each agent process.
// Payload limits reserve additional room for Go structures and runtime pages.
package agentbudget

const (
	ActiveNodes           = 256
	ActiveForwards        = 128
	ForwardUDPSessions    = 8192
	ForwardUDPPerListener = 1024
	QueueSlots            = 8192
	QueuePayloadBytes     = 3 << 20 // plus <1 MiB fixed ring storage
	PairEntriesPerTail    = 256     // both pairing maps, two tails: <2 MiB total
	ResidentGoBytes       = 64 << 20
	WorkerGoLimit         = "48MiB"
	NetworkGoLimit        = "32MiB"
	// Process-local compatibility for the pinned official Go-based core.
	SingBoxGoDebug  = "disablethp=1"
	ConfigBytes     = 2 << 20
	SettlementBytes = 1 << 20
	// Small Alpine host/container capacity cannot use larger controller defaults.
	AlpineSmallMemoryBytes = 384 << 20
	AlpineProxyMemoryMiB   = 96
	AlpineProxyGoMiB       = 64
)
