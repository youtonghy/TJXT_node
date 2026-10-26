package node

import (
	"net"
	"sort"

	"github.com/youtonghy/TJXT_node/api/panel"
)

// socketKinds returns whether a node type listens on TCP and on UDP.
func socketKinds(nodeType string) (tcp, udp bool) {
	switch nodeType {
	case "hysteria2", "tuic", "wireguard":
		return false, true
	case "shadowsocks", "tunnel":
		return true, true
	case "tun":
		return false, false
	default:
		return true, false
	}
}

// listenOverlaps reports whether two listen addresses can receive the same
// connections: equal, or one of them a wildcard address.
func listenOverlaps(a, b string) bool {
	wildcard := func(ip string) bool {
		parsed := net.ParseIP(ip)
		return ip == "" || parsed != nil && parsed.IsUnspecified()
	}
	return a == b || wildcard(a) || wildcard(b)
}

// portConflicts returns the nodes (by index) that listen on a port another node
// already uses without sharing its inbound, with the tag of that node. Nodes are
// considered by id, so the oldest node keeps the port.
func portConflicts(infos []*panel.NodeInfo) map[int]string {
	order := make([]int, 0, len(infos))
	for i, info := range infos {
		if info != nil && info.Common != nil {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return infos[order[a]].Id < infos[order[b]].Id })
	conflicts := map[int]string{}
	var kept []int
	for _, i := range order {
		info := infos[i]
		tcp, udp := socketKinds(info.Type)
		for _, j := range kept {
			other := infos[j]
			otherTCP, otherUDP := socketKinds(other.Type)
			if info.Common.ServerPort != other.Common.ServerPort || !(tcp && otherTCP || udp && otherUDP) ||
				!listenOverlaps(info.Common.ListenIP, other.Common.ListenIP) || info.Inbound() == other.Inbound() {
				continue
			}
			conflicts[i] = other.Tag
			break
		}
		if _, dropped := conflicts[i]; !dropped {
			kept = append(kept, i)
		}
	}
	return conflicts
}
