package overrides

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rackerlabs/genestack-cli/internal/model"
)

func testCluster() *model.Cluster {
	c := model.NewCluster("lab")
	c.Domain = "api.openstack.example.com"
	c.Region = "RegionTwo"
	return c
}

func find(files []File, suffix string) (File, bool) {
	for _, f := range files {
		if strings.HasSuffix(f.Rel(), suffix) {
			return f, true
		}
	}
	return File{}, false
}

func TestEndpointsGeneration(t *testing.T) {
	c := testCluster()
	f, ok := find(Managed(c), "global_overrides/endpoints.yaml")
	if !ok {
		t.Fatal("endpoints.yaml not generated")
	}
	s := string(f.Content)
	for _, want := range []string{
		"_region: &region RegionTwo",
		"host: nova.api.openstack.example.com",
		"host: keystone.api.openstack.example.com",
		"host: cinder.api.openstack.example.com", // volume/v2/v3 all map to cinder
		"region_name: *region",
		"public: 443",
		"public: https",
		"admin: 80", // identity admin port
	} {
		if !strings.Contains(s, want) {
			t.Errorf("endpoints.yaml missing %q", want)
		}
	}
}

func TestProbeAndKnobs(t *testing.T) {
	c := testCluster()
	files := Managed(c)

	probe, _ := find(files, "probe_targets.yaml")
	if !strings.Contains(string(probe.Content), "url: https://keystone.api.openstack.example.com") {
		t.Error("probe targets missing keystone url")
	}
	if !strings.Contains(string(probe.Content), "novnc.api.openstack.example.com/vnc_auto.html") {
		t.Error("probe targets missing novnc vnc_auto.html")
	}

	neutron, _ := find(files, "neutron/neutron-helm-overrides.yaml")
	if !strings.Contains(string(neutron.Content), "global_physnet_mtu: 9000") {
		t.Error("neutron MTU not rendered")
	}

	nova, _ := find(files, "nova/nova-helm-overrides.yaml")
	if !strings.Contains(string(nova.Content), "volume_use_multipath: true") {
		t.Error("nova multipath not rendered")
	}

	prom, _ := find(files, "kube-prometheus-stack/prometheus-helm-overrides.yaml")
	if !strings.Contains(string(prom.Content), "storage: 50Gi") {
		t.Error("prometheus storage not rendered")
	}
}

func TestPlanPassthroughOverrides(t *testing.T) {
	c := testCluster()
	dir := t.TempDir()

	// Override the generated neutron file and add a brand-new cinder file.
	mustWrite(t, filepath.Join(dir, "helm-configs/neutron/neutron-helm-overrides.yaml"), "custom: neutron\n")
	mustWrite(t, filepath.Join(dir, "helm-configs/cinder/cinder.yaml"), "custom: cinder\n")

	plan, err := Plan(c, dir)
	if err != nil {
		t.Fatal(err)
	}

	managed := len(Managed(c))  // 7
	if len(plan) != managed+1 { // +1 for the new cinder file
		t.Fatalf("plan has %d files, want %d", len(plan), managed+1)
	}

	neutron, _ := find(plan, "neutron/neutron-helm-overrides.yaml")
	if neutron.Source != Passthrough || !strings.Contains(string(neutron.Content), "custom: neutron") {
		t.Errorf("passthrough did not override generated neutron file: %+v", neutron.Source)
	}
	cinder, _ := find(plan, "cinder/cinder.yaml")
	if cinder.Source != Passthrough {
		t.Error("cinder passthrough file missing from plan")
	}
}

func TestCustomServiceHosts(t *testing.T) {
	c := testCluster()
	c.Overrides.Endpoints.Hosts = map[string]string{
		"keystone": "keystone-user4.ftigenestack.site",
		"skyline":  "cloud-user4.ftigenestack.site", // gateway-only
	}
	files := Managed(c)

	// Catalog layer: keystone overridden, un-set services keep <prefix>.<domain>.
	ep, ok := find(files, "global_overrides/endpoints.yaml")
	if !ok {
		t.Fatal("endpoints.yaml not generated")
	}
	eps := string(ep.Content)
	if !strings.Contains(eps, "host: keystone-user4.ftigenestack.site") {
		t.Error("endpoints.yaml did not use the custom keystone host")
	}
	if !strings.Contains(eps, "host: nova.api.openstack.example.com") {
		t.Error("endpoints.yaml should keep the default nova host")
	}

	// Gateway layer: patch script lists overridden services only.
	gw, ok := find(files, "gateway/patch-routes.sh")
	if !ok {
		t.Fatal("patch-routes.sh not generated")
	}
	gws := string(gw.Content)
	if !strings.Contains(gws, "patch httproute keystone") ||
		!strings.Contains(gws, "keystone-user4.ftigenestack.site") {
		t.Error("patch-routes.sh missing keystone route override")
	}
	if !strings.Contains(gws, "patch httproute skyline") ||
		!strings.Contains(gws, "cloud-user4.ftigenestack.site") {
		t.Error("patch-routes.sh missing skyline route override")
	}
	if strings.Contains(gws, "patch httproute nova") {
		t.Error("patch-routes.sh should not touch un-overridden services")
	}
}

func TestGatewayRoutesEmptyIsNoOp(t *testing.T) {
	gw, ok := find(Managed(testCluster()), "gateway/patch-routes.sh")
	if !ok {
		t.Fatal("patch-routes.sh not generated")
	}
	if strings.Contains(string(gw.Content), "patch httproute") {
		t.Errorf("expected a no-op script with no overrides, got:\n%s", gw.Content)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
