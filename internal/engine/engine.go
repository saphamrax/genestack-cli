// Package engine defines the ordered deployment phases (each a list of steps)
// that mirror the Genestack manual installation, plus the persistent run state
// that lets a deployment be resumed. Steps carry the exact shell command to run
// on the deployment host; execution itself is performed by the caller (the TUI)
// via the exec package, so this package stays free of I/O concerns beyond
// loading/saving state.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// helmAdopt returns a shell snippet that stamps Helm ownership metadata onto
// pre-created secrets so a chart can adopt them instead of failing with
// "invalid ownership metadata". Used where genestack's kubesecrets.yaml
// pre-creates a secret that an OpenStack-Helm chart also wants to own
// (e.g. nova-ssh, octavia-certificates). Idempotent and a no-op if absent.
func helmAdopt(release string, secrets ...string) string {
	return fmt.Sprintf(`for s in %s; do kubectl -n openstack get secret "$s" >/dev/null 2>&1 && { `+
		`kubectl -n openstack annotate secret "$s" meta.helm.sh/release-name=%s meta.helm.sh/release-namespace=openstack --overwrite; `+
		`kubectl -n openstack label secret "$s" app.kubernetes.io/managed-by=Helm --overwrite; }; done`,
		strings.Join(secrets, " "), release)
}

// RemoteInventoryPath is where the generated inventory is uploaded on the
// deployment host (matches the /etc/genestack symlink layout in the manual).
const RemoteInventoryPath = "/etc/genestack/inventory/inventory.yaml"

// Status is the lifecycle state of a step or phase.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Icon returns a single-rune status glyph for the TUI.
func (s Status) Icon() string {
	switch s {
	case StatusRunning:
		return "▶"
	case StatusDone:
		return "✓"
	case StatusFailed:
		return "✗"
	case StatusSkipped:
		return "-"
	default:
		return "·"
	}
}

// Step actions. An empty Action means the step runs Cmd on the deployment host.
// Non-empty actions are handled specially by the runner (they generate a file
// from the cluster model and upload it, rather than executing a shell command).
const (
	ActionUploadInventory  = "upload-inventory"
	ActionUploadOverrides  = "upload-overrides"
	ActionValidateNetworks = "validate-networks"
)

// Step is a single executable unit within a phase.
type Step struct {
	ID    string // stable identifier, used as the state key (e.g. "kubernetes.cluster")
	Title string // human-readable description
	Cmd   string // bash command executed on the deployment host (when Action is empty)
	// Action, when non-empty, selects a runner-handled action instead of Cmd
	// (see ActionUpload* constants).
	Action string
	// Optional marks steps that are not always required (e.g. Ceph, NetApp).
	// Optional steps default to skipped and are only run on explicit request.
	Optional bool
}

// Phase is an ordered group of steps representing a stage of the install.
type Phase struct {
	ID    string
	Title string
	Steps []Step
}

// DefaultPhases returns the full ordered deployment sequence derived from the
// manual. Commands are written to run on the deployment host where
// /opt/genestack lives. The generated inventory is uploaded out of band by the
// TUI before the host-setup phase.
func DefaultPhases() []Phase {
	const rc = "source /opt/genestack/scripts/genestack.rc"
	return []Phase{
		{
			ID: "config", Title: "Configure deployment",
			Steps: []Step{
				{ID: "config.clone", Title: "Clone genestack into /opt/genestack",
					Cmd: "test -d /opt/genestack/.git || git clone --recurse-submodules -j4 https://github.com/rackerlabs/genestack /opt/genestack"},
				{ID: "config.checkout", Title: "Checkout release-2025.4",
					Cmd: "cd /opt/genestack && git checkout release-2025.4"},
				{ID: "config.bootstrap", Title: "Run genestack bootstrap.sh",
					Cmd: "/opt/genestack/bootstrap.sh"},
				{ID: "config.opsbootstrap", Title: "Run openstack-ops bootstrap.sh",
					Cmd: "test -x /opt/openstack-ops/bootstrap.sh && /opt/openstack-ops/bootstrap.sh || echo 'openstack-ops not present, skipping'", Optional: true},
				{ID: "config.groupvars", Title: "Seed inventory group_vars + pin pod/service CIDRs",
					// genestack's seeded group_vars hardcode kube_pods_subnet
					// (10.236.0.0/14) which OVERRIDES the inventory.yaml value in
					// Ansible precedence. Write a last-alphabetically override file in
					// the k8s_cluster group so the model's CIDRs actually win.
					Cmd: `GV=/etc/genestack/inventory/group_vars; mkdir -p "$GV"; if [ -z "$(ls -A "$GV")" ]; then cp -r /opt/genestack/ansible/inventory/genestack/group_vars/. "$GV"/; fi; mkdir -p "$GV/k8s_cluster"; printf '%s\n' '---' '# genestack-cli: override genestack pod/service CIDR defaults (must win)' 'kube_pods_subnet: {{KUBE_PODS_SUBNET}}' 'kube_service_addresses: {{KUBE_SERVICE_CIDR}}' > "$GV/k8s_cluster/zz-genestack-cli.yml"; cat "$GV/k8s_cluster/zz-genestack-cli.yml"`},
				{ID: "config.inventory", Title: "Generate & upload inventory.yaml",
					Action: ActionUploadInventory},
				{ID: "config.overrides", Title: "Generate & upload override files",
					Action: ActionUploadOverrides},
			},
		},
		{
			ID: "host-setup", Title: "Host setup",
			Steps: []Step{
				{ID: "host.hostname", Title: "Set hostnames from inventory",
					Cmd: rc + " && ansible -m shell -a 'hostnamectl set-hostname {{ inventory_hostname }}' --become all"},
				{ID: "host.setup", Title: "Run host-setup.yml",
					Cmd: rc + " && cd /opt/genestack/ansible/playbooks && ansible-playbook host-setup.yml"},
				{ID: "host.configure", Title: "Run openstack-ops configure playbooks (if present)", Optional: true,
					Cmd: "if [ -d /opt/openstack-ops/playbooks ]; then cd /opt/openstack-ops/playbooks && /root/.venvs/openstack-ops/bin/ansible-playbook configure-hosts.yml configure-bash-environment.yml configure-packagemanager.yml; else echo 'openstack-ops not present, skipping'; fi"},
				{ID: "host.upgrade", Title: "apt update & dist-upgrade all nodes",
					Cmd: rc + " && ansible all -m ansible.builtin.apt -a 'update_cache=true upgrade=dist'"},
				{ID: "host.reboot", Title: "Reboot nodes to apply new kernel (excludes the deployer; waits for return)",
					// {{DEPLOY_NODE}} is the deployer's own inventory name when it is
					// also a cluster node. Rebooting it in-band would kill ansible /
					// the control session ("new session: EOF"), so exclude it here and
					// reboot it explicitly via host.reboot.self.
					Cmd: rc + ` && if [ -n "{{DEPLOY_NODE}}" ]; then ansible 'all:!{{DEPLOY_NODE}}' -m reboot --become; else ansible all -m reboot --become; fi`},
				{ID: "host.reboot.self", Title: "Reboot the deployer node itself (kills this session)", Optional: true,
					// Opt-in (Optional => skipped on phase/--all runs). Schedules the
					// reboot so the command returns first; reconnect afterwards and
					// resume with `genestack run kubernetes`.
					Cmd: `if [ -n "{{DEPLOY_NODE}}" ]; then echo "rebooting deployer {{DEPLOY_NODE}} in 1 min — reconnect, then: genestack run kubernetes"; shutdown -r +1; else echo "deployer is not a cluster node — nothing to reboot"; fi`},
			},
		},
		{
			ID: "kubernetes", Title: "Kubernetes (kubespray)",
			Steps: []Step{
				{ID: "k8s.preflight", Title: "Validate pod/service CIDRs (no overlap with node IPs)",
					// kube_pods_subnet/kube_service_addresses are baked into the cluster
					// at kubeadm init and are painful to change later — block the deploy
					// here if they overlap each other or any node/bridge IP.
					Action: ActionValidateNetworks},
				{ID: "k8s.kubectl", Title: "Install kubectl binaries",
					Cmd: `cd /opt/genestack/submodules/kubespray && curl -fsSLO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl`},
				{ID: "k8s.cluster", Title: "Deploy cluster (cluster.yml, ~15-20m)",
					Cmd: rc + " && cd /opt/genestack/submodules/kubespray && ansible-playbook cluster.yml"},
				{ID: "k8s.kubeconfig", Title: "Fetch kubeconfig to the deployment host",
					Cmd: rc + ` && mkdir -p /root/.kube /etc/kubernetes && ansible kube_control_plane[0] -m fetch -a 'src=/etc/kubernetes/admin.conf dest=/tmp/gs-kubeconfig flat=yes' && CP=$(ansible kube_control_plane[0] -m debug -a 'var=ip' 2>/dev/null | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' | head -1) && sed "s#server: https://[0-9.]*:6443#server: https://${CP}:6443#" /tmp/gs-kubeconfig | tee /root/.kube/config > /etc/kubernetes/admin.conf && kubectl get nodes`},
				{ID: "k8s.label.control", Title: "Label & untaint control plane nodes",
					Cmd: `kubectl label node {{CONTROLLER_NODES}} openstack-control-plane=enabled node-role.kubernetes.io/worker=worker --overwrite && { kubectl taint nodes -l openstack-control-plane=enabled node-role.kubernetes.io/control-plane:NoSchedule- || true; }`},
				{ID: "k8s.label.compute", Title: "Label compute & network nodes",
					Cmd: `kubectl label node {{COMPUTE_NODES}} openstack-compute-node=enabled openstack-network-node=enabled --overwrite && kubectl label node {{CONTROLLER_NODES}} openstack-network-node=enabled openstack-storage-node=enabled --overwrite`},
			},
		},
		{
			ID: "kube-ovn", Title: "CNI: Kube-OVN",
			Steps: []Step{
				{ID: "ovn.config", Title: "Copy kube-ovn helm overrides & set overlay iface + CIDRs",
					// IFACE = geneve tunnel endpoint (keeps the NIC's L3). VLAN_INTERFACE_NAME
					// is the kube-ovn vlan/underlay provider NIC, which gets bridged into OVS —
					// blank it out so kube-ovn never swallows a node interface (genestack does
					// provider networking via its own OVN, not kube-ovn's vlan provider).
					// POD_CIDR/SVC_CIDR must match the kubespray kube_pods_subnet /
					// kube_service_addresses (else kube-ovn allocates from a different
					// range than k8s expects). genestack's base file ships its own
					// defaults (e.g. POD_CIDR 10.236.0.0/14), so sync them from the model.
					Cmd: `F=/etc/genestack/helm-configs/kube-ovn/kube-ovn-helm-overrides.yaml; mkdir -p "$(dirname "$F")"; cp -n /opt/genestack/base-helm-configs/kube-ovn/kube-ovn-helm-overrides.yaml "$F" 2>/dev/null || true; sed -i -E 's#^([[:space:]]*IFACE:[[:space:]]*).*#\1"{{KUBE_OVN_IFACE}}"#; s#^([[:space:]]*VLAN_INTERFACE_NAME:[[:space:]]*).*#\1""#; s#^([[:space:]]*MIRROR_IFACE:[[:space:]]*).*#\1"mirror0"#; s#^([[:space:]]*DPDK_TUNNEL_IFACE:[[:space:]]*).*#\1"br-phy"#; s#^([[:space:]]*POD_CIDR:[[:space:]]*).*#\1"{{KUBE_PODS_SUBNET}}"#; s#^([[:space:]]*SVC_CIDR:[[:space:]]*).*#\1"{{KUBE_SERVICE_CIDR}}"#' "$F"; grep -nE '^[[:space:]]*(IFACE|VLAN_INTERFACE_NAME|MIRROR_IFACE|DPDK_TUNNEL_IFACE|POD_CIDR|SVC_CIDR):' "$F"`},
				{ID: "ovn.labels", Title: "Label nodes for Kube-OVN",
					Cmd: `kubectl label node -l beta.kubernetes.io/os=linux kubernetes.io/os=linux --overwrite && kubectl label node -l node-role.kubernetes.io/control-plane kube-ovn/role=master --overwrite && kubectl label node -l ovn.kubernetes.io/ovs_dp_type!=userspace ovn.kubernetes.io/ovs_dp_type=kernel --overwrite`},
				{ID: "ovn.install", Title: "Run install-kube-ovn.sh",
					Cmd: "/opt/genestack/bin/install-kube-ovn.sh"},
			},
		},
		{
			ID: "metallb", Title: "MetalLB",
			Steps: []Step{
				{ID: "metallb.ns", Title: "Apply metallb namespace",
					Cmd: "kubectl apply -f /etc/genestack/manifests/metallb/metallb-namespace.yaml"},
				{ID: "metallb.install", Title: "Run install-metallb.sh (wait for CRDs + controller)",
					Cmd: "/opt/genestack/bin/install-metallb.sh && kubectl wait --for=condition=established crd/ipaddresspools.metallb.io crd/l2advertisements.metallb.io --timeout=180s && kubectl -n metallb-system rollout status deploy/metallb-controller --timeout=180s"},
				{ID: "metallb.lb", Title: "Apply MetalLB service pools",
					Cmd: "kubectl apply -f /etc/genestack/manifests/metallb/metallb-openstack-service-lb.yml"},
			},
		},
		{
			ID: "openstack-ns", Title: "OpenStack namespace & secrets",
			Steps: []Step{
				{ID: "osns.create", Title: "Create openstack namespace",
					Cmd: "kubectl apply -k /etc/genestack/kustomize/openstack/base"},
				{ID: "osns.secrets", Title: "Generate service secrets",
					Cmd: "/opt/genestack/bin/create-secrets.sh --region {{REGION}} && /opt/genestack/bin/create-skyline-secrets.sh"},
				{ID: "osns.deploy", Title: "Deploy generated secrets",
					// kubesecrets.yaml has a secret in the prometheus namespace; ensure it
					// exists first. Use apply (not create) so the step is re-runnable.
					Cmd: "kubectl get namespace prometheus >/dev/null 2>&1 || kubectl create namespace prometheus; kubectl apply -f /etc/genestack/kubesecrets.yaml"},
			},
		},
		{
			ID: "longhorn", Title: "Longhorn storage",
			Steps: []Step{
				{ID: "longhorn.label", Title: "Label storage nodes",
					Cmd: "kubectl label node -l node-role.kubernetes.io/control-plane longhorn.io/storage-node=enabled --overwrite"},
				{ID: "longhorn.ns", Title: "Apply Longhorn namespace",
					Cmd: "kubectl apply -f /etc/genestack/manifests/longhorn/longhorn-namespace.yaml"},
				{ID: "longhorn.install", Title: "Run install-longhorn.sh",
					Cmd: "/opt/genestack/bin/install-longhorn.sh"},
				{ID: "longhorn.sc", Title: "Apply storage classes",
					Cmd: "kubectl apply -f /etc/genestack/manifests/longhorn/longhorn-general-storageclass.yaml && kubectl apply -f /etc/genestack/manifests/longhorn/longhorn-general-multi-attach-storageclass.yaml"},
			},
		},
		{
			ID: "monitoring", Title: "Monitoring (Prometheus stack)",
			Steps: []Step{
				// Must run before gateway/openstack: provides the ServiceMonitor /
				// PrometheusRule CRDs that their charts reference. Needs the `general`
				// storage class from Longhorn for the prometheus PV.
				{ID: "mon.prometheus", Title: "Install kube-prometheus-stack (+ wait for CRDs)",
					Cmd: "/opt/genestack/bin/install-kube-prometheus-stack.sh && kubectl wait --for=condition=established crd/servicemonitors.monitoring.coreos.com crd/prometheusrules.monitoring.coreos.com --timeout=300s"},
				{ID: "mon.loki", Title: "Install Loki (requires S3 override)", Optional: true,
					Cmd: "/opt/genestack/bin/install-loki.sh"},
				{ID: "mon.fluentbit", Title: "Install Fluentbit (after Loki)", Optional: true,
					Cmd: "/opt/genestack/bin/install-fluentbit.sh"},
			},
		},
		{
			ID: "gateway", Title: "Gateway API (Envoy)",
			Steps: []Step{
				{ID: "gw.install", Title: "Install Envoy Gateway",
					// install-envoy-gateway.sh returns 0 even when its helm install fails,
					// so verify a deployment actually landed (unmask the failure).
					Cmd: "/opt/genestack/bin/install-envoy-gateway.sh && kubectl -n envoyproxy-gateway-system get deploy -o name | grep -q ."},
				{ID: "gw.wait", Title: "Wait for gateway deployments",
					Cmd: "kubectl wait deployment --for=condition=Available --all --namespace envoyproxy-gateway-system --timeout=600s"},
				{ID: "gw.setup", Title: "Setup flex gateway (HTTP routes)",
					Cmd: "/opt/genestack/bin/setup-envoy-gateway.sh --email openstack-enterprise-ops@rackspace.com --domain {{DOMAIN}}"},
				{ID: "gw.routes", Title: "Apply custom gateway hostnames", Optional: true,
					// Overrides spec.hostnames on the genestack-created HTTPRoutes for
					// any service in overrides.endpoints.hosts. No-op when none are set.
					// Requires `overrides upload` to have shipped the generated script.
					Cmd: "bash /etc/genestack/manifests/gateway/patch-routes.sh"},
			},
		},
		{
			ID: "infra", Title: "Infrastructure services",
			Steps: []Step{
				{ID: "infra.memcached", Title: "Install memcached",
					Cmd: "/opt/genestack/bin/install-memcached.sh"},
				{ID: "infra.libvirt", Title: "Install libvirt",
					Cmd: "/opt/genestack/bin/install-libvirt.sh"},
				{ID: "infra.mariadb.op", Title: "Install MariaDB operator",
					Cmd: "/opt/genestack/bin/install-mariadb-operator.sh && kubectl wait deployment --for=condition=Available --all --namespace mariadb-system --timeout=600s"},
				{ID: "infra.mariadb.cluster", Title: "Deploy MariaDB Galera cluster",
					Cmd: "kubectl --namespace openstack apply -k /etc/genestack/kustomize/mariadb-cluster/galera"},
				{ID: "infra.rabbitmq.op", Title: "Install RabbitMQ operators",
					Cmd: "kubectl apply -k /etc/genestack/kustomize/rabbitmq-operator/base && kubectl apply -k /etc/genestack/kustomize/rabbitmq-topology-operator/base"},
				{ID: "infra.rabbitmq.cluster", Title: "Deploy RabbitMQ cluster",
					Cmd: "kubectl apply -k /etc/genestack/kustomize/rabbitmq-cluster/overlay"},
			},
		},
		{
			ID: "ovn-config", Title: "OVN annotations",
			Steps: []Step{
				{ID: "ovnc.bridges", Title: "Annotate bridges",
					Cmd: `kubectl annotate nodes -l openstack-network-node=enabled ovn.openstack.org/int_bridge='{{OVN_INT_BRIDGE}}' --overwrite && kubectl annotate nodes -l openstack-network-node=enabled ovn.openstack.org/bridges='{{OVN_BRIDGES}}' --overwrite`},
				{ID: "ovnc.ports", Title: "Assign ports & mappings",
					Cmd: `kubectl annotate nodes -l openstack-network-node=enabled ovn.openstack.org/ports='{{OVN_PORTS}}' --overwrite && kubectl annotate nodes -l openstack-network-node=enabled ovn.openstack.org/mappings='{{OVN_MAPPINGS}}' --overwrite && kubectl annotate nodes -l openstack-network-node=enabled ovn.openstack.org/availability_zones='{{OVN_AZ}}' --overwrite`},
				{ID: "ovnc.gateway", Title: "Mark gateways & install OVN",
					Cmd: `kubectl annotate nodes -l openstack-network-node=enabled ovn.openstack.org/gateway='enabled' --overwrite && kubectl apply -k /etc/genestack/kustomize/ovn/base`},
			},
		},
		{
			ID: "openstack", Title: "OpenStack services",
			Steps: []Step{
				{ID: "os.keystone", Title: "Install Keystone",
					Cmd: "/opt/genestack/bin/install-keystone.sh && kubectl --namespace openstack apply -f /etc/genestack/manifests/utils/utils-openstack-client-admin.yaml"},
				{ID: "os.storage", Title: "Apply storage backend manifests (if any)",
					// Self-skipping: applies operator-provided backend secrets /
					// ConfigMaps (e.g. Ceph keyrings, ceph-etc) dropped under
					// overrides/manifests/storage/ before Glance/Cinder install. No-op
					// when the dir is absent. Uses if/then/else so a real apply failure
					// fails the step (a trailing `|| echo` would mask it).
					Cmd: `if [ -d /etc/genestack/manifests/storage ]; then kubectl apply -f /etc/genestack/manifests/storage/; else echo "no storage manifests, skipping"; fi`},
				{ID: "os.glance", Title: "Install Glance",
					Cmd: "/opt/genestack/bin/install-glance.sh"},
				{ID: "os.cinder", Title: "Install Cinder API (+ configured backends)",
					Cmd: "/opt/genestack/bin/install-cinder.sh"},
				{ID: "os.placement", Title: "Install Placement",
					Cmd: "/opt/genestack/bin/install-placement.sh"},
				{ID: "os.nova", Title: "Install Nova",
					Cmd: helmAdopt("nova", "nova-ssh") + " ; /opt/genestack/bin/install-nova.sh"},
				{ID: "os.neutron", Title: "Install Neutron",
					Cmd: "/opt/genestack/bin/install-neutron.sh"},
				{ID: "os.barbican", Title: "Install Barbican",
					Cmd: "/opt/genestack/bin/install-barbican.sh"},
				{ID: "os.skyline", Title: "Install Skyline",
					Cmd: "/opt/genestack/bin/install-skyline.sh"},
				{ID: "os.heat", Title: "Install Heat",
					Cmd: "/opt/genestack/bin/install-heat.sh"},
				{ID: "os.octavia", Title: "Install Octavia", Optional: true,
					Cmd: helmAdopt("octavia", "octavia-certificates") + " ; /opt/genestack/bin/install-octavia.sh"},
			},
		},
		{
			ID: "post", Title: "Post-install",
			Steps: []Step{
				{ID: "post.endpoints", Title: "List endpoint catalog",
					Cmd: "kubectl --namespace openstack exec -ti openstack-admin-client -- openstack endpoint list"},
				{ID: "post.images", Title: "Configure glance/nova/neutron (openstack-ops)",
					Cmd: "cd /opt/openstack-ops/playbooks && /root/.venvs/openstack-ops/bin/ansible-playbook configure-glance.yml configure-nova.yml configure-neutron.yml", Optional: true},
			},
		},
	}
}

// State persists per-step status to a JSON file so runs can be resumed.
type State struct {
	path    string
	Steps   map[string]Status `json:"steps"`
	Updated time.Time         `json:"updated"`
}

// LoadState reads run state from path, returning an empty state if absent.
func LoadState(path string) *State {
	s := &State{path: path, Steps: map[string]Status{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, s)
	if s.Steps == nil {
		s.Steps = map[string]Status{}
	}
	s.path = path
	return s
}

// Get returns the stored status for a step ID, defaulting to pending.
func (s *State) Get(id string) Status {
	if st, ok := s.Steps[id]; ok {
		return st
	}
	return StatusPending
}

// Set records a status for a step and saves to disk (best effort).
func (s *State) Set(id string, st Status) {
	s.Steps[id] = st
	s.Updated = time.Now()
	s.save()
}

// Delete clears the stored status for a step (resetting it to pending) and
// saves to disk.
func (s *State) Delete(id string) {
	delete(s.Steps, id)
	s.Updated = time.Now()
	s.save()
}

func (s *State) save() {
	if s.path == "" {
		return
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, b, 0o644)
}

// PhaseStatus aggregates the statuses of a phase's steps into a single status
// and a done/total count (optional steps that are pending are not counted as
// outstanding).
func (s *State) PhaseStatus(p Phase) (status Status, done, total int) {
	var anyRunning, anyFailed, anyPending bool
	for _, st := range p.Steps {
		total++
		cur := s.Get(st.ID)
		switch cur {
		case StatusDone:
			done++
		case StatusSkipped:
			done++
		case StatusRunning:
			anyRunning = true
		case StatusFailed:
			anyFailed = true
		default:
			if st.Optional {
				// optional pending steps don't block phase completion
				done++
			} else {
				anyPending = true
			}
		}
	}
	switch {
	case anyFailed:
		return StatusFailed, done, total
	case anyRunning:
		return StatusRunning, done, total
	case anyPending:
		return StatusPending, done, total
	default:
		return StatusDone, done, total
	}
}
