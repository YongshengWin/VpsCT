package agentproto

import "errors"

// ValidateBillingSelection is repeated on the agent at the actual cutover.
func ValidateBillingSelection(snapshot *NetworkSnapshot, policy NetworkBillingPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.Mode == "legacy" {
		return nil
	}
	if snapshot == nil || snapshot.Status != "ok" {
		return errors.New("需要完整的网卡采集结果才能选择计费网卡")
	}
	for _, id := range policy.InterfaceIDs {
		found := false
		for _, n := range snapshot.Interfaces {
			if n.ID == id && n.CountersValid {
				found = true
				break
			}
		}
		if !found {
			return errors.New("所选网卡不存在或计数不可用")
		}
	}
	return ValidateBillingTopology(snapshot, policy)
}

// Missing interfaces are handled by the meter separately. Known overlapping
// layers must never be billed together, including after topology changes.
func ValidateBillingTopology(snapshot *NetworkSnapshot, policy NetworkBillingPolicy) error {
	if snapshot == nil || policy.Mode != "interfaces" {
		return nil
	}
	byID := map[string]NetworkInterface{}
	byIndex := map[int]NetworkInterface{}
	for _, n := range snapshot.Interfaces {
		byID[n.ID] = n
		byIndex[n.Index] = n
	}
	selected := map[int]bool{}
	for _, id := range policy.InterfaceIDs {
		n, ok := byID[id]
		if !ok {
			continue
		}
		if n.Kind == "loopback" {
			return errors.New("回环接口不能作为服务器计费来源")
		}
		selected[n.Index] = true
		if len(policy.InterfaceIDs) > 1 {
			switch n.Kind {
			case "wireguard", "tun", "ipip", "gre", "gretap", "sit", "vxlan", "ip6tnl":
				return errors.New("隧道与承载网卡可能重复计数，请选择同一个计量层")
			}
		}
	}
	for index := range selected {
		seen := map[int]bool{index: true}
		queue := []int{byIndex[index].ParentIndex, byIndex[index].MasterIndex}
		for len(queue) > 0 {
			p := queue[0]
			queue = queue[1:]
			if p == 0 || seen[p] {
				continue
			}
			seen[p] = true
			if selected[p] {
				return errors.New("所选网卡存在上下级关系，会重复计数")
			}
			if n, ok := byIndex[p]; ok {
				queue = append(queue, n.ParentIndex, n.MasterIndex)
			}
		}
	}
	return nil
}
