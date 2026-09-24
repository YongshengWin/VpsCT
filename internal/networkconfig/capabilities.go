package networkconfig

const MitaVersion = 1
const MitaHeader = "X-Ctlvps-Mita-Version"
const BillingVersion = 1
const WireGuardVersion = 1
const WireGuardHeader = "X-Ctlvps-Network-WireGuard-Version"
const SSHVersion = 1
const SSHHeader = "X-Ctlvps-Network-SSH-Version"

// ExecutableCapabilities is a local, side-effect-free preflight contract for
// already verified binaries. It is separate from live host diagnostics.
type ExecutableCapabilities struct {
	Mita             int `json:"mita"`
	Schema           int `json:"schema"`
	NetworkBinding   int `json:"network_binding"`
	NetworkEgress    int `json:"network_egress"`
	NetworkSSH       int `json:"network_ssh"`
	NetworkWireGuard int `json:"network_wireguard"`
	NetworkForward   int `json:"network_forward"`
	NetworkBilling   int `json:"network_billing"`
	ListenBinding    int `json:"listen_binding"`
	ForwardDNS       int `json:"forward_dns"`
	ForwardPrivate   int `json:"forward_private"`
	ForwardTransport int `json:"forward_transport"`
}

func CurrentCapabilities() ExecutableCapabilities {
	return ExecutableCapabilities{Schema: 1, Mita: MitaVersion, NetworkBinding: BindingVersion, NetworkEgress: EgressVersion, NetworkSSH: SSHVersion, NetworkWireGuard: WireGuardVersion, NetworkForward: ForwardVersion, NetworkBilling: BillingVersion, ListenBinding: 1, ForwardDNS: 1, ForwardPrivate: 1, ForwardTransport: 1}
}
