// Package overrides renders genestack helm-override / config files from the
// cluster model (the "generated" layer) and merges them with operator-authored
// passthrough files (the local overrides/ directory). Generated files cover the
// domain-derived configuration (endpoints, probe targets, grafana, barbican)
// and a few common typed knobs (neutron MTU, nova multipath, prometheus PV);
// passthrough files cover everything else and take precedence on path clashes.
package overrides

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rackerlabs/genestack-cli/internal/model"
)

// RemoteBase is the directory on the deployment host that mirrors the local
// overrides/ directory and holds all generated files.
const RemoteBase = "/etc/genestack"

// Source records where a planned file came from.
type Source string

const (
	Generated   Source = "generated"
	Passthrough Source = "passthrough"
)

// File is a rendered override destined for Path on the deployment host.
type File struct {
	Path    string // absolute remote path, e.g. /etc/genestack/helm-configs/...
	Content []byte
	Source  Source
}

// Rel returns the path relative to RemoteBase (for display / local mirroring).
func (f File) Rel() string {
	return strings.TrimPrefix(strings.TrimPrefix(f.Path, RemoteBase), "/")
}

// Managed returns the files the CLI generates from the cluster model.
func Managed(c *model.Cluster) []File {
	files := []File{
		endpoints(c),
		probeTargets(c),
		grafana(c),
		barbican(c),
		gatewayRoutes(c),
		neutron(c),
		nova(c),
		prometheus(c),
		metallb(c),
	}
	// Storage is free-form: render overrides.cinder / overrides.glance verbatim
	// when set. Backend-agnostic — Ceph, NetApp, Pure, … are all just config.
	if f, ok := rawHelmConfig("cinder", c.Overrides.Cinder); ok {
		files = append(files, f)
	}
	if f, ok := rawHelmConfig("glance", c.Overrides.Glance); ok {
		files = append(files, f)
	}
	return files
}

func path(rel string) string { return RemoteBase + "/" + rel }

// host derives a service public FQDN, e.g. "nova" -> "nova.<domain>". An
// explicit per-service override in overrides.endpoints.hosts wins.
func host(c *model.Cluster, prefix string) string {
	if h := c.Overrides.Endpoints.Hosts[prefix]; h != "" {
		return h
	}
	return prefix + "." + c.Domain
}

// --- domain-derived generators (group A) ---

// epSvc maps an openstack-helm endpoints key to a host prefix and the port key
// used under endpoints.<key>.port.
type epSvc struct {
	key, prefix, portKey string
}

// endpointServices is the set rendered into endpoints.yaml, mirroring the
// manual's global_overrides/endpoints.yaml. identity is emitted separately.
var endpointServices = []epSvc{
	{"compute", "nova", "api"},
	{"compute_metadata", "metadata", "metadata"},
	{"compute_novnc_proxy", "novnc", "novnc_proxy"},
	{"cloudformation", "cloudformation", "api"},
	{"cloudwatch", "cloudwatch", "api"},
	{"container_infra", "magnum", "api"},
	{"key_manager", "barbican", "api"},
	{"dashboard", "horizon", "web"},
	{"metric", "gnocchi", "api"},
	{"image", "glance", "api"},
	{"load_balancer", "octavia", "api"},
	{"network", "neutron", "api"},
	{"orchestration", "heat", "api"},
	{"placement", "placement", "api"},
	{"volume", "cinder", "api"},
	{"volumev2", "cinder", "api"},
	{"volumev3", "cinder", "api"},
}

// identityAuthServices get a region_name under identity.auth.
var identityAuthServices = []string{
	"admin", "barbican", "cinder", "ceilometer", "glance", "gnocchi",
	"heat", "heat_trustee", "heat_stack_user", "ironic", "magnum",
	"neutron", "nova", "placement", "octavia",
}

func endpoints(c *model.Cluster) File {
	scheme := c.Overrides.Endpoints.Scheme
	port := c.Overrides.Endpoints.Port

	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }

	w("---\n")
	w("_region: &region %s\n\n", c.Region)
	w("clusterDomain: %s\n\n", c.K8s.ClusterName)
	w("pod:\n  resources:\n    enabled: false\n\n")
	w("endpoints:\n")

	for _, s := range endpointServices {
		w("  %s:\n", s.key)
		w("    host_fqdn_override:\n      public:\n        tls: {}\n        host: %s\n", host(c, s.prefix))
		w("    port:\n      %s:\n        public: %d\n", s.portKey, port)
		w("    scheme:\n      public: %s\n", scheme)
	}

	// identity is special: auth region block + admin port.
	w("  identity:\n")
	w("    auth:\n")
	for _, svc := range identityAuthServices {
		w("      %s:\n        region_name: *region\n", svc)
	}
	w("    host_fqdn_override:\n      public:\n        tls: {}\n        host: %s\n", host(c, "keystone"))
	w("    port:\n      api:\n        public: %d\n        admin: 80\n", port)
	w("    scheme:\n      public: %s\n", scheme)

	// ingress has no host override, just the public port.
	w("  ingress:\n    port:\n      ingress:\n        public: %d\n", port)

	return File{Path: path("helm-configs/global_overrides/endpoints.yaml"), Content: []byte(b.String()), Source: Generated}
}

// probeTargetServices are probed by the blackbox exporter.
var probeTargetServices = []string{
	"nova", "neutron", "keystone", "octavia", "glance", "heat", "cinder",
	"cloudformation", "placement", "barbican", "magnum", "masakari",
}

func probeTargets(c *model.Cluster) File {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("serviceMonitor:\n  targets:\n")
	w("    # Baseline external probe\n")
	w("    - name: google-probe\n      url: https://google.com\n")
	w("    # OpenStack services\n")
	for _, s := range probeTargetServices {
		w("    - name: %s-probe\n      url: https://%s\n", s, host(c, s))
	}
	w("    - name: novnc-probe\n      url: https://%s/vnc_auto.html\n", host(c, "novnc"))
	return File{Path: path("helm-configs/prometheus-blackbox-exporter/probe_targets.yaml"), Content: []byte(b.String()), Source: Generated}
}

func grafana(c *model.Cluster) File {
	content := fmt.Sprintf("custom_host: %s\n", host(c, "grafana"))
	return File{Path: path("helm-configs/grafana/grafana-helm-overrides.yaml"), Content: []byte(content), Source: Generated}
}

func barbican(c *model.Cluster) File {
	// Additive host override matching the openstack-helm pattern. If the
	// genestack-provided barbican override carries more settings, supply the
	// full file via the passthrough directory (it wins on path clash).
	content := fmt.Sprintf("---\nendpoints:\n  key_manager:\n    host_fqdn_override:\n      public:\n        host: %s\n", host(c, "barbican"))
	return File{Path: path("helm-configs/barbican/barbican-helm-overrides.yaml"), Content: []byte(content), Source: Generated}
}

// gwRoute is the namespace + HTTPRoute name genestack creates for a service.
type gwRoute struct{ namespace, name string }

// openstackGatewayServices are the services whose genestack route is
// custom-<svc>-gateway-route in the openstack namespace (from `kubectl -n
// openstack get httproute`).
var openstackGatewayServices = map[string]bool{
	"barbican": true, "blazar": true, "cinder": true, "cloudformation": true,
	"cloudkitty": true, "freezer": true, "glance": true, "gnocchi": true,
	"heat": true, "ironic": true, "keystone": true, "magnum": true,
	"manila": true, "masakari": true, "metadata": true, "neutron": true,
	"nova": true, "novnc": true, "octavia": true, "placement": true,
	"skyline": true, "zaqar": true,
}

// gatewayRouteFor returns the namespace/route genestack created for a service
// key (as used in overrides.endpoints.hosts), or ok=false if none is known.
// Confirm names against the cluster with `kubectl get httproute -A`.
func gatewayRouteFor(svc string) (gwRoute, bool) {
	switch svc {
	case "grafana":
		return gwRoute{"grafana", "grafana-gateway-route"}, true
	case "prometheus", "alertmanager":
		return gwRoute{"prometheus", "custom-" + svc + "-gateway-route"}, true
	}
	if openstackGatewayServices[svc] {
		return gwRoute{"openstack", "custom-" + svc + "-gateway-route"}, true
	}
	return gwRoute{}, false
}

// GatewayHostServices returns the service keys that have a known gateway route,
// sorted. Used for validation.
func GatewayHostServices() []string {
	keys := []string{"grafana", "prometheus", "alertmanager"}
	for k := range openstackGatewayServices {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// gatewayGatewayNS / gatewayName identify the Envoy Gateway genestack creates.
const (
	gatewayGatewayNS = "envoy-gateway"
	gatewayName      = "flex-gateway"
)

// gatewayRoutes emits a script that points an overridden service at its custom
// FQDN on BOTH layers genestack builds: the HTTPRoute (custom-<svc>-gateway-route)
// AND the matching Gateway listener (<svc>-https) on flex-gateway. It swaps the
// default "<svc>.<domain>" hostname for the override, preserving any other
// hostnames. cert-manager (annotation-driven on the Gateway) then reissues the
// TLS cert for the new hostname automatically. No-op when no overrides are set.
func gatewayRoutes(c *model.Cluster) File {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("#!/usr/bin/env bash\n")
	w("# Generated by genestack-cli — overrides per-service gateway hostnames\n")
	w("# (HTTPRoute + Gateway listener). Re-run `overrides upload` after editing\n")
	w("# overrides.endpoints.hosts. cert-manager reissues certs for the new names.\n")
	w("set -uo pipefail\n")
	w("command -v jq >/dev/null || { echo 'ERROR: jq is required'; exit 1; }\n\n")
	// Swap one hostname on an HTTPRoute's spec.hostnames, keeping the rest.
	w("patch_route() { # ns route old-host new-host\n")
	w("  local h\n")
	w("  h=$(kubectl -n \"$1\" get httproute \"$2\" -o json 2>/dev/null | jq -c --arg o \"$3\" --arg n \"$4\" '[.spec.hostnames[] | if . == $o then $n else . end]') || { echo \"WARN: route $1/$2 not found\"; return; }\n")
	w("  [ -n \"$h\" ] && kubectl -n \"$1\" patch httproute \"$2\" --type=merge -p \"{\\\"spec\\\":{\\\"hostnames\\\":$h}}\" || echo \"WARN: failed to patch route $1/$2\"\n")
	w("}\n")
	// Swap the matching listener's hostname on the Gateway; cert-manager reissues.
	w("patch_listener() { # old-host new-host\n")
	w("  local l\n")
	w("  l=$(kubectl -n %s get gateway %s -o json 2>/dev/null | jq -c --arg o \"$1\" --arg n \"$2\" '[.spec.listeners[] | if .hostname == $o then .hostname = $n else . end]') || { echo \"WARN: gateway %s/%s not found\"; return; }\n", gatewayGatewayNS, gatewayName, gatewayGatewayNS, gatewayName)
	w("  [ -n \"$l\" ] && kubectl -n %s patch gateway %s --type=merge -p \"{\\\"spec\\\":{\\\"listeners\\\":$l}}\" || echo \"WARN: failed to patch gateway listener for $1\"\n", gatewayGatewayNS, gatewayName)
	w("}\n\n")

	for _, svc := range sortedKeys(c.Overrides.Endpoints.Hosts) {
		fqdn := c.Overrides.Endpoints.Hosts[svc]
		if fqdn == "" {
			continue
		}
		rt, ok := gatewayRouteFor(svc)
		if !ok {
			w("echo \"WARN: no known gateway route for service %q (skipped)\"\n", svc)
			continue
		}
		old := svc + "." + c.Domain
		w("patch_route %s %s %q %q\n", rt.namespace, rt.name, old, fqdn)
		w("patch_listener %q %q\n", old, fqdn)
	}

	return File{Path: path("manifests/gateway/patch-routes.sh"), Content: []byte(b.String()), Source: Generated}
}

// sortedKeys returns the map keys in deterministic order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- typed knobs ---

func neutron(c *model.Cluster) File {
	n := c.Overrides.Neutron
	content := fmt.Sprintf(`conf:
  neutron:
    DEFAULT:
      global_physnet_mtu: %d
  plugins:
    ml2_conf:
      ml2:
        path_mtu: %d
        physical_network_mtus: physnet1:%d
`, n.GlobalPhysnetMTU, n.PathMTU, n.Physnet1MTU)
	return File{Path: path("helm-configs/neutron/neutron-helm-overrides.yaml"), Content: []byte(content), Source: Generated}
}

func nova(c *model.Cluster) File {
	content := fmt.Sprintf(`---
conf:
  nova:
    libvirt:
      volume_use_multipath: %t

  enable_iscsi: %t
`, c.Overrides.Nova.Multipath, c.Overrides.Nova.EnableISCSI)
	return File{Path: path("helm-configs/nova/nova-helm-overrides.yaml"), Content: []byte(content), Source: Generated}
}

// rawHelmConfig renders a free-form override body (overrides.cinder / .glance)
// verbatim into helm-configs/<svc>/<svc>-helm-overrides.yaml. ok is false when
// the node is empty (the operator did not set it).
func rawHelmConfig(svc string, node yaml.Node) (File, bool) {
	if node.Kind == 0 {
		return File{}, false
	}
	body, err := yaml.Marshal(&node)
	if err != nil {
		return File{}, false
	}
	content := "---\n" + string(body)
	return File{Path: path("helm-configs/" + svc + "/" + svc + "-helm-overrides.yaml"), Content: []byte(content), Source: Generated}, true
}

func prometheus(c *model.Cluster) File {
	content := fmt.Sprintf(`defaultRules:
  additionalRuleLabels: {}
alertmanager:
  logLevel: info
prometheus:
  prometheusSpec:
    scrapeConfigSelector:
      matchLabels:
        prometheus: system-monitoring-prometheus
    storageSpec:
      volumeClaimTemplate:
        spec:
          resources:
            requests:
              storage: %s
`, c.Overrides.Prometheus.Storage)
	return File{Path: path("helm-configs/kube-prometheus-stack/prometheus-helm-overrides.yaml"), Content: []byte(content), Source: Generated}
}

// metallb renders the MetalLB IPAddressPools + L2Advertisements applied at the
// metallb phase. External and internal (primary) pools are advertised from the
// configured interface on worker nodes, mirroring the manual.
func metallb(c *model.Cluster) File {
	mb := c.MetalLB
	content := fmt.Sprintf(`---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: gateway-api-external
  namespace: metallb-system
spec:
  addresses:
  - %s
  autoAssign: false
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: openstack-external-advertisement
  namespace: metallb-system
spec:
  ipAddressPools:
  - gateway-api-external
  nodeSelectors:
  - matchLabels:
      node-role.kubernetes.io/worker: worker
  interfaces:
  - %s
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: primary
  namespace: metallb-system
spec:
  addresses:
  - %s
  autoAssign: false
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: openstack-internal-advertisement
  namespace: metallb-system
spec:
  ipAddressPools:
  - primary
  nodeSelectors:
  - matchLabels:
      node-role.kubernetes.io/worker: worker
  interfaces:
  - %s
`, mb.ExternalPool, mb.Interface, mb.InternalPool, mb.Interface)
	return File{Path: path("manifests/metallb/metallb-openstack-service-lb.yml"), Content: []byte(content), Source: Generated}
}

// --- passthrough + plan ---

// LoadPassthrough reads operator-authored files under dir, mapping each to the
// remote path RemoteBase/<relative path>. A missing dir yields no files.
func LoadPassthrough(dir string) ([]File, error) {
	var out []File
	if dir == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, File{
			Path:    RemoteBase + "/" + filepath.ToSlash(rel),
			Content: content,
			Source:  Passthrough,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Plan merges generated and passthrough files into the final set to upload,
// sorted by remote path. Passthrough files override generated ones on the same
// path.
func Plan(c *model.Cluster, passthroughDir string) ([]File, error) {
	merged := map[string]File{}
	for _, f := range Managed(c) {
		merged[f.Path] = f
	}
	pass, err := LoadPassthrough(passthroughDir)
	if err != nil {
		return nil, err
	}
	for _, f := range pass {
		merged[f.Path] = f
	}

	paths := make([]string, 0, len(merged))
	for p := range merged {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	out := make([]File, 0, len(paths))
	for _, p := range paths {
		out = append(out, merged[p])
	}
	return out, nil
}
