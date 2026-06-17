// Package model defines the cluster data model used to describe a Genestack
// OpenStack deployment: the deployment (jump) host, the target nodes, their
// roles and the cluster-wide variables. The model is persisted to a single
// cluster.yaml file and is the source of truth for inventory generation.
package model

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Role is a logical responsibility assigned to a node. A node may carry more
// than one role. Roles map onto one or more Ansible inventory groups via
// GroupMembers.
type Role string

const (
	// RoleController hosts the Kubernetes control plane, etcd and the
	// OpenStack control plane. By convention controllers also act as OVN
	// network nodes and cinder storage nodes unless those roles are split out.
	RoleController Role = "controller"
	// RoleCompute hosts nova-compute and is a plain Kubernetes worker.
	RoleCompute Role = "compute"
	// RoleNetwork marks a node as an OVN gateway / network node. Implied by
	// controller unless explicitly separated.
	RoleNetwork Role = "network"
	// RoleStorage marks a node as a cinder storage node. Implied by controller.
	RoleStorage Role = "storage"
)

// AllRoles is the set of roles the CLI understands.
var AllRoles = []Role{RoleController, RoleCompute, RoleNetwork, RoleStorage}

// ValidRole reports whether r is a role the CLI understands.
func ValidRole(r Role) bool {
	for _, x := range AllRoles {
		if x == r {
			return true
		}
	}
	return false
}

// Bridge names referenced by the manual (netplan + MTU tests). The values are
// per-node IP addresses and are informational for now (used by MTU checks and
// future netplan generation).
const (
	BridgeHost    = "br-host"
	BridgeStorage = "br-storage"
	BridgeRepl    = "br-repl"
	BridgeOverlay = "br-overlay"
	BridgePXE     = "br-pxe"
)

// Node is a single target host in the deployment.
type Node struct {
	Name        string            `yaml:"name"`              // inventory_hostname, e.g. user01-ctl01
	AnsibleHost string            `yaml:"ansible_host"`      // IP reached over SSH from the deployment host (e.g. WAN)
	NodeIP      string            `yaml:"node_ip,omitempty"` // kubespray k8s node IP (ip/access_ip); the private cluster/overlay IP
	Roles       []Role            `yaml:"roles"`             // one or more roles
	Bridges     map[string]string `yaml:"bridges,omitempty"` // optional bridge name -> IP (br-host, br-overlay, ...)
}

// HasRole reports whether the node carries the given role.
func (n Node) HasRole(r Role) bool {
	for _, x := range n.Roles {
		if x == r {
			return true
		}
	}
	return false
}

// IsController is a convenience for the controller role.
func (n Node) IsController() bool { return n.HasRole(RoleController) }

// IsCompute is a convenience for the compute role.
func (n Node) IsCompute() bool { return n.HasRole(RoleCompute) }

// Deployment describes how to reach the deployment host where all playbooks,
// scripts and kubectl commands are executed. With Mode "local" the CLI runs
// directly on this host; otherwise it drives the host over SSH.
type Deployment struct {
	Mode    string `yaml:"mode,omitempty"`     // "ssh" (default) or "local"
	Host    string `yaml:"host"`               // hostname/IP of the deployment node (SSH mode)
	User    string `yaml:"user"`               // SSH user (typically root)
	Port    int    `yaml:"port"`               // SSH port, default 22
	KeyPath string `yaml:"key_path,omitempty"` // optional private key path; SSH agent is used otherwise
}

// IsLocal reports whether commands should run on the local machine rather than
// over SSH.
func (d Deployment) IsLocal() bool {
	return strings.EqualFold(d.Mode, "local")
}

// K8sVars holds the cluster-wide Kubernetes variables emitted under
// children.k8s_cluster.vars in the inventory.
type K8sVars struct {
	ClusterName        string   `yaml:"cluster_name"`
	KubeOVNIface       string   `yaml:"kube_ovn_iface"`
	KubeServiceCIDR    string   `yaml:"kube_service_addresses"`
	KubePodsSubnet     string   `yaml:"kube_pods_subnet"`
	UpstreamDNSServers []string `yaml:"upstream_dns_servers,omitempty"`
}

// DefaultK8sVars returns the values used in the manual as a sensible starting
// point.
func DefaultK8sVars() K8sVars {
	return K8sVars{
		ClusterName:     "cluster.local",
		KubeOVNIface:    "br-overlay",
		KubeServiceCIDR: "10.233.0.0/18",
		KubePodsSubnet:  "10.236.0.0/14",
	}
}

// MetalLBConfig describes the IP pools announced by MetalLB. The manual splits
// a network into an external pool (gateway VIP) and an internal/primary pool,
// both advertised from a physical interface (typically br-host) on worker nodes.
type MetalLBConfig struct {
	ExternalPool string `yaml:"external_pool"` // e.g. 10.230.0.10/32
	InternalPool string `yaml:"internal_pool"` // e.g. 10.230.0.128/25
	Interface    string `yaml:"interface"`     // e.g. br-host
}

// DefaultMetalLB returns the example values from the manual.
func DefaultMetalLB() MetalLBConfig {
	return MetalLBConfig{
		ExternalPool: "10.230.0.10/32",
		InternalPool: "10.230.0.128/25",
		Interface:    "br-host",
	}
}

func (m *MetalLBConfig) fillDefaults() {
	d := DefaultMetalLB()
	if m.ExternalPool == "" {
		m.ExternalPool = d.ExternalPool
	}
	if m.InternalPool == "" {
		m.InternalPool = d.InternalPool
	}
	if m.Interface == "" {
		m.Interface = d.Interface
	}
}

// OVNConfig holds the OVN node annotations applied to network/compute nodes.
// These are site/hardware specific (the physical bond, provider network name).
type OVNConfig struct {
	IntBridge        string `yaml:"int_bridge"`        // e.g. br-int
	Bridges          string `yaml:"bridges"`           // e.g. br-ex,br-pxe
	Ports            string `yaml:"ports"`             // e.g. br-ex:bond1
	Mappings         string `yaml:"mappings"`          // e.g. physnet1:br-ex
	AvailabilityZone string `yaml:"availability_zone"` // e.g. az1
}

// DefaultOVN returns the example values from the manual.
func DefaultOVN() OVNConfig {
	return OVNConfig{
		IntBridge:        "br-int",
		Bridges:          "br-ex,br-pxe",
		Ports:            "br-ex:bond1",
		Mappings:         "physnet1:br-ex",
		AvailabilityZone: "az1",
	}
}

func (o *OVNConfig) fillDefaults() {
	d := DefaultOVN()
	if o.IntBridge == "" {
		o.IntBridge = d.IntBridge
	}
	if o.Bridges == "" {
		o.Bridges = d.Bridges
	}
	if o.Ports == "" {
		o.Ports = d.Ports
	}
	if o.Mappings == "" {
		o.Mappings = d.Mappings
	}
	if o.AvailabilityZone == "" {
		o.AvailabilityZone = d.AvailabilityZone
	}
}

// EndpointsOverride tunes the generated global_overrides/endpoints.yaml. The
// per-service public host is derived as "<prefix>.<domain>" unless an explicit
// FQDN is given for that service in Hosts.
type EndpointsOverride struct {
	Scheme string `yaml:"scheme"` // public scheme, e.g. https
	Port   int    `yaml:"port"`   // public port, e.g. 443
	// Hosts overrides the public FQDN per service, keyed by the service prefix
	// (e.g. keystone, glance, neutron, skyline, harbor). A service absent from
	// the map falls back to "<prefix>.<domain>".
	Hosts map[string]string `yaml:"hosts,omitempty"`
}

// NeutronOverride tunes neutron MTU settings.
type NeutronOverride struct {
	GlobalPhysnetMTU int `yaml:"global_physnet_mtu"` // e.g. 9000
	PathMTU          int `yaml:"path_mtu"`           // e.g. 1558
	Physnet1MTU      int `yaml:"physnet1_mtu"`       // e.g. 1500
}

// NovaOverride tunes nova volume handling.
type NovaOverride struct {
	Multipath   bool `yaml:"volume_use_multipath"`
	EnableISCSI bool `yaml:"enable_iscsi"`
}

// PrometheusOverride tunes the prometheus PV size (the chart default of 15Gi is
// generally too small).
type PrometheusOverride struct {
	Storage string `yaml:"storage"` // e.g. 50Gi
}

// Overrides holds the typed knobs the CLI renders into genestack helm-override
// files. Domain-derived files (endpoints, probe targets, grafana, barbican) use
// Cluster.Domain/Region directly. Anything not modelled here can be supplied as
// a passthrough file under the local overrides/ directory.
//
// Cinder and Glance are intentionally NOT typed: storage backends (Ceph, NetApp,
// Pure, …) vary too much. They are free-form YAML rendered verbatim into the
// helm-override files, so a new backend is pure config (no code). Backend secrets
// either sit inline here (cluster.yaml is gitignored) or as passthrough
// manifests under overrides/manifests/storage/ (applied by the os.storage step).
type Overrides struct {
	Endpoints  EndpointsOverride  `yaml:"endpoints"`
	Neutron    NeutronOverride    `yaml:"neutron"`
	Nova       NovaOverride       `yaml:"nova"`
	Prometheus PrometheusOverride `yaml:"prometheus"`
	// Free-form helm-override bodies rendered to helm-configs/<svc>/<svc>-helm-overrides.yaml.
	Cinder  yaml.Node `yaml:"cinder,omitempty"`
	Glance  yaml.Node `yaml:"glance,omitempty"`
	Libvirt yaml.Node `yaml:"libvirt,omitempty"` // e.g. the external-Ceph libvirt secret
	// Manifests is a list of raw Kubernetes objects (ConfigMaps/Secrets, e.g. the
	// Ceph ceph-etc ConfigMap + RBD keyring Secrets) rendered into a single file
	// under manifests/storage/ and applied by the os.storage step — so backend
	// wiring can live in cluster.yaml without a separate passthrough folder.
	Manifests []yaml.Node `yaml:"manifests,omitempty"`
}

// DefaultOverrides returns the recommended values from the manual.
func DefaultOverrides() Overrides {
	return Overrides{
		Endpoints:  EndpointsOverride{Scheme: "https", Port: 443},
		Neutron:    NeutronOverride{GlobalPhysnetMTU: 9000, PathMTU: 1558, Physnet1MTU: 1500},
		Nova:       NovaOverride{Multipath: true, EnableISCSI: true},
		Prometheus: PrometheusOverride{Storage: "50Gi"},
	}
}

// fillDefaults populates zero-valued numeric/string fields from the defaults so
// hand-written configs that omit (parts of) the overrides block still render.
// Booleans are left as parsed.
func (o *Overrides) fillDefaults() {
	d := DefaultOverrides()
	if o.Endpoints.Scheme == "" {
		o.Endpoints.Scheme = d.Endpoints.Scheme
	}
	if o.Endpoints.Port == 0 {
		o.Endpoints.Port = d.Endpoints.Port
	}
	if o.Neutron.GlobalPhysnetMTU == 0 {
		o.Neutron.GlobalPhysnetMTU = d.Neutron.GlobalPhysnetMTU
	}
	if o.Neutron.PathMTU == 0 {
		o.Neutron.PathMTU = d.Neutron.PathMTU
	}
	if o.Neutron.Physnet1MTU == 0 {
		o.Neutron.Physnet1MTU = d.Neutron.Physnet1MTU
	}
	if o.Prometheus.Storage == "" {
		o.Prometheus.Storage = d.Prometheus.Storage
	}
}

// Cluster is the top-level configuration object persisted to cluster.yaml.
// DefaultGenestackVersion is the genestack git ref (release tag/branch) checked
// out into /opt/genestack when cluster.yaml does not pin one.
const DefaultGenestackVersion = "release-2025.4"

type Cluster struct {
	Name             string        `yaml:"name"`
	Region           string        `yaml:"region"`                      // e.g. RegionOne
	Domain           string        `yaml:"domain"`                      // OpenStack API domain, e.g. api.openstack.example.com
	GenestackVersion string        `yaml:"genestack_version,omitempty"` // genestack git ref to checkout, e.g. release-2025.4
	AnsiblePort      int           `yaml:"ansible_port"`                // ansible_port for all hosts
	Deployment       Deployment    `yaml:"deployment"`
	K8s              K8sVars       `yaml:"k8s"`
	MetalLB          MetalLBConfig `yaml:"metallb"`
	OVN              OVNConfig     `yaml:"ovn"`
	Overrides        Overrides     `yaml:"overrides"`
	Nodes            []Node        `yaml:"nodes"`
}

// NewCluster returns a Cluster populated with manual-derived defaults.
func NewCluster(name string) *Cluster {
	return &Cluster{
		Name:             name,
		Region:           "RegionOne",
		Domain:           "api.openstack.example.com",
		GenestackVersion: DefaultGenestackVersion,
		AnsiblePort:      22,
		Deployment:       Deployment{User: "root", Port: 22},
		K8s:              DefaultK8sVars(),
		MetalLB:          DefaultMetalLB(),
		OVN:              DefaultOVN(),
		Overrides:        DefaultOverrides(),
	}
}

// Node returns the node with the given name, or nil.
func (c *Cluster) Node(name string) *Node {
	for i := range c.Nodes {
		if c.Nodes[i].Name == name {
			return &c.Nodes[i]
		}
	}
	return nil
}

// AddNode appends a node, returning an error if the name already exists.
func (c *Cluster) AddNode(n Node) error {
	if c.Node(n.Name) != nil {
		return fmt.Errorf("node %q already exists", n.Name)
	}
	if n.Name == "" || n.AnsibleHost == "" {
		return fmt.Errorf("node requires a name and ansible_host")
	}
	for _, r := range n.Roles {
		if !ValidRole(r) {
			return fmt.Errorf("invalid role %q", r)
		}
	}
	c.Nodes = append(c.Nodes, n)
	return nil
}

// RemoveNode deletes a node by name, reporting whether it existed.
func (c *Cluster) RemoveNode(name string) bool {
	for i := range c.Nodes {
		if c.Nodes[i].Name == name {
			c.Nodes = append(c.Nodes[:i], c.Nodes[i+1:]...)
			return true
		}
	}
	return false
}

// Inventory group names produced for the kubespray/genestack inventory.
const (
	GroupKubeControlPlane     = "kube_control_plane"
	GroupEtcd                 = "etcd"
	GroupKubeNode             = "kube_node"
	GroupOpenStackControl     = "openstack_control_plane"
	GroupOVNNetworkNodes      = "ovn_network_nodes"
	GroupCinderStorageNodes   = "cinder_storage_nodes"
	GroupOpenStackComputeNode = "openstack_compute_nodes"
)

// GroupOrder is the order groups are emitted in the inventory file.
var GroupOrder = []string{
	GroupKubeControlPlane,
	GroupEtcd,
	GroupKubeNode,
	GroupOpenStackControl,
	GroupOVNNetworkNodes,
	GroupCinderStorageNodes,
	GroupOpenStackComputeNode,
}

// GroupMembers returns the sorted node names that belong to the given inventory
// group, derived from node roles. The mapping mirrors the manual:
//
//	controller -> kube_control_plane, etcd, kube_node, openstack_control_plane,
//	              ovn_network_nodes, cinder_storage_nodes
//	compute    -> kube_node, openstack_compute_nodes
//	network    -> ovn_network_nodes (explicit split)
//	storage    -> cinder_storage_nodes (explicit split)
func (c *Cluster) GroupMembers(group string) []string {
	var out []string
	for _, n := range c.Nodes {
		if c.inGroup(n, group) {
			out = append(out, n.Name)
		}
	}
	sort.Strings(out)
	return out
}

func (c *Cluster) inGroup(n Node, group string) bool {
	ctrl := n.IsController()
	switch group {
	case GroupKubeControlPlane, GroupEtcd, GroupOpenStackControl:
		return ctrl
	case GroupKubeNode:
		// Every target host joins the Kubernetes cluster as a worker — OVN and
		// cinder pods can only be scheduled on nodes that are kube_node members.
		_ = ctrl
		return len(n.Roles) > 0
	case GroupOVNNetworkNodes:
		return ctrl || n.HasRole(RoleNetwork)
	case GroupCinderStorageNodes:
		return ctrl || n.HasRole(RoleStorage)
	case GroupOpenStackComputeNode:
		return n.IsCompute()
	}
	return false
}

// Validate performs basic sanity checks before generating inventory or running.
func (c *Cluster) Validate() error {
	if len(c.Nodes) == 0 {
		return fmt.Errorf("no nodes defined")
	}
	if len(c.GroupMembers(GroupKubeControlPlane)) == 0 {
		return fmt.Errorf("at least one controller node is required")
	}
	seen := map[string]bool{}
	for _, n := range c.Nodes {
		if seen[n.Name] {
			return fmt.Errorf("duplicate node name %q", n.Name)
		}
		seen[n.Name] = true
		if len(n.Roles) == 0 {
			return fmt.Errorf("node %q has no roles", n.Name)
		}
	}
	return nil
}

// Load reads a cluster configuration from path.
func Load(path string) (*Cluster, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Cluster
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.GenestackVersion == "" {
		c.GenestackVersion = DefaultGenestackVersion
	}
	if c.AnsiblePort == 0 {
		c.AnsiblePort = 22
	}
	if c.Deployment.Port == 0 {
		c.Deployment.Port = 22
	}
	c.MetalLB.fillDefaults()
	c.OVN.fillDefaults()
	c.Overrides.fillDefaults()
	return &c, nil
}

// Save writes the cluster configuration to path.
func (c *Cluster) Save(path string) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
