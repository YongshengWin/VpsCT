package networkconfig

const MitaVersion = 1
const MitaHeader = "X-Ctlvps-Mita-Version"
const BillingVersion = 1
const WireGuardVersion = 1
const WireGuardHeader = "X-Ctlvps-Network-WireGuard-Version"
const SSHVersion = 1
const SSHHeader = "X-Ctlvps-Network-SSH-Version"
const NATVersion = 2
const ForwardUDPVersion = 2
const ForwardUDPHeader = "X-Ctlvps-Forward-UDP-Version"

// ExecutableCapabilities is a local, side-effect-free preflight contract for
// already verified binaries. It is separate from live host diagnostics.
type ExecutableCapabilities struct {
	OpenRCMieru         int `json:"openrc_mieru"`
	OpenRCMaintenance   int `json:"openrc_maintenance"`
	OpenRC              int `json:"openrc"`
	NAT                 int `json:"nat"`
	Mita                int `json:"mita"`
	Schema              int `json:"schema"`
	NetworkBinding      int `json:"network_binding"`
	NetworkEgress       int `json:"network_egress"`
	NetworkSSH          int `json:"network_ssh"`
	NetworkWireGuard    int `json:"network_wireguard"`
	NetworkForward      int `json:"network_forward"`
	NetworkBilling      int `json:"network_billing"`
	ListenBinding       int `json:"listen_binding"`
	ForwardDNS          int `json:"forward_dns"`
	ForwardPrivate      int `json:"forward_private"`
	ForwardTransport    int `json:"forward_transport"`
	ForwardUDP          int `json:"forward_udp"`
	ForwardUDPIsolation int `json:"forward_udp_isolation,omitempty"`
}

func CurrentCapabilities() ExecutableCapabilities {
	return ExecutableCapabilities{OpenRCMieru: 1, OpenRCMaintenance: 1, OpenRC: 1, NAT: NATVersion, Schema: 1, Mita: MitaVersion, NetworkBinding: BindingVersion, NetworkEgress: EgressVersion, NetworkSSH: SSHVersion, NetworkWireGuard: WireGuardVersion, NetworkForward: ForwardVersion, NetworkBilling: BillingVersion, ListenBinding: 1, ForwardDNS: 1, ForwardPrivate: 1, ForwardTransport: 1, ForwardUDP: 1, ForwardUDPIsolation: 1}
}
