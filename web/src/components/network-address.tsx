import type { NetworkView } from "@/lib/types";
import { addressKey, networkAddresses, parseAddressKey, type NetworkAddress } from "@/lib/network";
import { Select } from "@/components/ui";

export function NetworkAddressSelect({ value, onChange, inventory, family, interfaceID, label, optional = true }: { value?: NetworkAddress; onChange: (v?: NetworkAddress) => void; inventory?: NetworkView; family?: "ipv4" | "ipv6"; interfaceID?: string; label: string; optional?: boolean }) {
  const options = networkAddresses(inventory, family, interfaceID);
  const selected = addressKey(value);
  return <Select aria-label={label} value={selected} onChange={(e) => onChange(parseAddressKey(e.target.value))}>
    <option value="">{optional ? "自动选择源地址" : "请选择本机地址"}</option>
    {selected && !options.some((o) => o.value === selected) && <option value={selected}>{value?.address}（当前清单中不可用）</option>}
    {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
  </Select>;
}
