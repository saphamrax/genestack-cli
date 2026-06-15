// Package inventory renders a model.Cluster into the Ansible/kubespray
// inventory.yaml format expected by genestack (see the "Ansible configuration"
// section of the manual). The output is produced deterministically with a
// manual writer so group ordering and the `null` host markers match the manual
// exactly.
package inventory

import (
	"fmt"
	"strings"

	"github.com/rackerlabs/genestack-cli/internal/model"
)

// Generate returns the inventory.yaml content for the cluster.
func Generate(c *model.Cluster) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	// all.vars and all.hosts
	w("all:\n")
	w("  vars:\n")
	w("    ansible_port: %d\n", c.AnsiblePort)
	w("  hosts:\n")
	for _, n := range c.Nodes {
		w("    %s:\n", n.Name)
		w("      ansible_host: %s\n", n.AnsibleHost)
		// kubespray binds k8s components (kubelet, etcd, apiserver advertise) to
		// `ip`/`access_ip`. Set them to the private cluster IP so the cluster
		// rides the internal network even when SSH uses a different (WAN) address.
		if n.NodeIP != "" {
			w("      ip: %s\n", n.NodeIP)
			w("      access_ip: %s\n", n.NodeIP)
		}
	}

	// children.k8s_cluster
	w("  children:\n")
	w("    k8s_cluster:\n")
	w("      vars:\n")
	w("        cluster_name: %s\n", c.K8s.ClusterName)
	w("        kube_ovn_iface: %s\n", c.K8s.KubeOVNIface)
	w("        kube_ovn_default_interface_name: %s\n", c.K8s.KubeOVNIface)
	w("        kube_ovn_central_hosts: \"{{ groups['%s'] }}\"\n", model.GroupOVNNetworkNodes)
	w("        kube_service_addresses: %s\n", c.K8s.KubeServiceCIDR)
	w("        kube_pods_subnet: %s\n", c.K8s.KubePodsSubnet)
	if len(c.K8s.UpstreamDNSServers) > 0 {
		w("        upstream_dns_servers:\n")
		for _, dns := range c.K8s.UpstreamDNSServers {
			w("          - \"%s\"\n", dns)
		}
	}

	w("      children:\n")
	for _, g := range model.GroupOrder {
		members := c.GroupMembers(g)
		w("        %s:\n", g)
		// openstack_compute_nodes carries extra iSCSI/multipath vars per the manual.
		if g == model.GroupOpenStackComputeNode {
			w("          vars:\n")
			w("            enable_iscsi: true\n")
			w("            custom_multipath: false\n")
		}
		if len(members) == 0 {
			w("          hosts: {}\n")
			continue
		}
		w("          hosts:\n")
		for _, name := range members {
			w("            %s: null\n", name)
		}
	}

	return []byte(b.String()), nil
}
