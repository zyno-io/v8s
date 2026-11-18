package fake

import (
	"encoding/json"
	"fmt"
	"net/netip"
)

// IP plan of the fake clusters: node i is 10.0.(i/200).(10+i%200) and owns the pod CIDR 10.64.0.0 + i*512 /23;
// a pod is base + 2 + host, where host counts the pods created on its node. Services use 10.96.0.0/12, the
// MetalLB pool is 192.168.100.0/24 and the macvlan network 192.168.50.0/24.

func nodeIP(i int) string { return fmt.Sprintf("10.0.%d.%d", i/200, 10+i%200) }

func nodeExternalIP(i int) string { return fmt.Sprintf("203.0.113.%d", 20+i) }

func addrOf(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func nodePodCIDRBase(i int) netip.Addr {
	return addrOf(uint32(10)<<24 | uint32(64)<<16 + uint32(i)*512)
}

func nodePodCIDR(i int) string { return nodePodCIDRBase(i).String() + "/23" }

// podIP is the address of the pod numbered host on node.
func podIP(node int, host uint16) string {
	return addrOf(uint32(10)<<24 | uint32(64)<<16 + uint32(node)*512 + 2 + uint32(host)).String()
}

// nodeCNIAddr is the Calico VXLAN tunnel address of node i (the pod CIDR base + 1).
func nodeCNIAddr(i int) string { return nodePodCIDRBase(i).Next().String() }

const multusNet = "team-1/macvlan-conf"

// podNetworkStatus is the Multus annotation of a pod attached to the macvlan network.
func podNetworkStatus(ip string, host uint16) string {
	b, _ := json.Marshal([]map[string]any{
		{"name": "k8s-pod-network", "ips": []string{ip}, "default": true},
		{"name": multusNet, "interface": "net1", "ips": []string{fmt.Sprintf("192.168.50.%d", 2+int(host)%250)}},
	})
	return string(b)
}
