export interface UnavailablePort {
  server_id: number;
  port: number;
  transport: "tcp" | "udp" | "both";
  network: string;
  reason: string;
  created_at: string;
  updated_at: string;
}
export const portTransportLabels = { tcp: "TCP", udp: "UDP", both: "TCP + UDP" };
