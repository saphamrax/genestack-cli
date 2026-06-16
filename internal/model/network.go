package model

import (
	"fmt"
	"net"
)

// CheckNetworks validates the Kubernetes pod/service CIDRs against each other
// and against every node/bridge IP, returning all problems found. These CIDRs
// are fixed at kubeadm init, so an overlap (e.g. a pod CIDR that contains the
// node management IPs) breaks the cluster and is hard to undo — callers use
// this to gate the kubernetes deploy.
func (c *Cluster) CheckNetworks() []string {
	var errs []string

	pods, podErr := parseCIDR("k8s.kube_pods_subnet", c.K8s.KubePodsSubnet, &errs)
	svc, svcErr := parseCIDR("k8s.kube_service_addresses", c.K8s.KubeServiceCIDR, &errs)

	if podErr == nil && svcErr == nil && netsOverlap(pods, svc) {
		errs = append(errs, fmt.Sprintf("kube_pods_subnet %s overlaps kube_service_addresses %s",
			c.K8s.KubePodsSubnet, c.K8s.KubeServiceCIDR))
	}

	// A node/bridge IP falling inside a pod or service CIDR means cluster
	// traffic would be routed into the overlay — the lab07 failure mode.
	check := func(label, ip string) {
		if ip == "" {
			return
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			errs = append(errs, fmt.Sprintf("%s: %q is not a valid IP", label, ip))
			return
		}
		if podErr == nil && pods.Contains(parsed) {
			errs = append(errs, fmt.Sprintf("%s %s is inside kube_pods_subnet %s", label, ip, c.K8s.KubePodsSubnet))
		}
		if svcErr == nil && svc.Contains(parsed) {
			errs = append(errs, fmt.Sprintf("%s %s is inside kube_service_addresses %s", label, ip, c.K8s.KubeServiceCIDR))
		}
	}

	for _, n := range c.Nodes {
		check(fmt.Sprintf("node %q ansible_host", n.Name), n.AnsibleHost)
		check(fmt.Sprintf("node %q node_ip", n.Name), n.NodeIP)
		for br, ip := range n.Bridges {
			check(fmt.Sprintf("node %q bridge %s", n.Name, br), ip)
		}
	}

	return errs
}

// parseCIDR parses a CIDR, appending a descriptive error (and returning it) when
// the value is empty or malformed.
func parseCIDR(label, cidr string, errs *[]string) (*net.IPNet, error) {
	if cidr == "" {
		err := fmt.Errorf("empty")
		*errs = append(*errs, fmt.Sprintf("%s is required", label))
		return nil, err
	}
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s %q is not a valid CIDR", label, cidr))
		return nil, err
	}
	return n, nil
}

// netsOverlap reports whether two IP networks share any address.
func netsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}
