# CLAUDE.md

Context for working on **genestack-cli**. Read this first.

## What this is

A Go TUI/CLI that deploys OpenStack via [Genestack](https://github.com/rackerlabs/genestack).
It models nodes/roles, generates the kubespray `inventory.yaml` and the helm
override / manifest files, and runs the install as **resumable phases** over SSH
(or locally on the deployment host). It wraps the Rackspace manual install
(`docs/manual-installation.html`) — that HTML is the source of truth for what the
real steps are.

- Module: `github.com/rackerlabs/genestack-cli` · Go 1.26
- Full operator guide: `docs/DEPLOYMENT.md` · user docs: `README.md`
- Default command (no args) launches the TUI.

## Build / test / run

```bash
make build           # -> ./genestack   (go build -o genestack ./cmd/genestack)
make build-all       # cross-compile to dist/ (linux/darwin, amd64/arm64)
make test            # go test ./...   (keep green)
make check           # fmt + vet + test
go run ./cmd/genestack --config cluster.yaml phases
```

The linux binary is statically linked (drop it on any deployment host). Always
`gofmt -w ./cmd ./internal` after edits; `make test` must stay green.

## Architecture (internal/)

| Package | Responsibility |
|---------|----------------|
| `model` | `Cluster`/`Node`/`Deployment`/`K8sVars`/`MetalLBConfig`/`OVNConfig`/`Overrides`; load/save `cluster.yaml`; role→inventory-group mapping (`GroupMembers`); defaults via `fillDefaults`/`Default*`. |
| `inventory` | Renders `inventory.yaml` with a deterministic string builder (order matters, not yaml.Marshal). Emits `ip`/`access_ip` per node when `node_ip` set. |
| `overrides` | Generates helm-override/manifest files from the model (endpoints, metallb pools, neutron MTU, nova, prometheus, grafana, blackbox probes, barbican) + loads operator **passthrough** files from local `overrides/` dir. `Plan()` merges them; **passthrough wins** on path clash. **Storage** is backend-agnostic: `overrides.cinder`/`overrides.glance` are free-form `yaml.Node`s rendered verbatim (via `rawHelmConfig`) into `helm-configs/{cinder,glance}/<svc>-helm-overrides.yaml` only when set — so Ceph/NetApp/Pure/… are pure config, no per-backend code. Backend secrets/ConfigMaps go as passthrough under `overrides/manifests/storage/`, applied by the self-skipping `os.storage` step before glance/cinder. Inline secrets live in the gitignored `cluster.yaml`. |
| `engine` | `Phase`/`Step`/`Status`, `DefaultPhases()` (the pipeline), JSON run-state (`.<config>.state.json`), `helmAdopt()`, `RemoteInventoryPath`. A `Step` runs a shell `Cmd` **or** an `Action` (`upload-inventory`/`upload-overrides`/`validate-networks`). `Optional` steps are skipped on phase/`--all` runs. The `kubernetes` phase starts with `k8s.preflight` (`validate-networks`) which blocks the deploy if pod/service CIDRs overlap each other or any node/bridge IP. |
| `exec` | `Executor` interface; `New(Config)` returns SSH (`ssh.go`) or Local (`local.go`) by `cfg.Local`. SSH uses `key_path` **exclusively** when set (else agent) to avoid MaxAuthTries. |
| `runner` | Executes a step via the Executor; `Expand()` substitutes `{{PLACEHOLDERS}}`; handles the upload Actions. Holds `*model.Cluster` + `OverridesDir`. |
| `runlog` | Per-run logs: `logs/<timestamp>/run.log` + `<step-id>.log`. nil-safe (`--no-log` passes nil). |
| `tui` | Bubble Tea 3-pane dashboard (nodes/phases/logs). |

`cmd/genestack/`: `main.go` (dispatch + global flags), `run.go` (run/reset/
phases/steps/status/connect/inventory/buildRunner), `overrides_cmd.go`,
`help.go` (overview + per-command help).

## Pipeline (13 phases)

`config → host-setup → kubernetes → kube-ovn → metallb → openstack-ns →
longhorn → monitoring → gateway → infra → ovn-config → openstack → post`

Run them with `genestack run <phase|step> [flags]`. State persists, so re-runs
skip done steps. `genestack phases` / `genestack steps [phase]` show status.

## Placeholders (expanded by `runner.Expand`)

`{{REGION}}` `{{DOMAIN}}` `{{OVN_INT_BRIDGE}}` `{{OVN_BRIDGES}}` `{{OVN_PORTS}}`
`{{OVN_MAPPINGS}}` `{{OVN_AZ}}` `{{KUBE_OVN_IFACE}}` `{{KUBE_PODS_SUBNET}}`
`{{KUBE_SERVICE_CIDR}}` `{{CONTROLLER_NODES}}`
`{{COMPUTE_NODES}}` `{{DEPLOY_NODE}}` — all sourced from `cluster.yaml`. Use these
in step `Cmd`s instead of hardcoding site values or fragile `awk` node-name
matching. `{{DEPLOY_NODE}}` is the deployer's own inventory name when the
deployment host is also a cluster node (else ""); see the reboot gotcha below.

## cluster.yaml (model) — key fields

```yaml
name, region, domain, ansible_port
deployment: {mode: ssh|local, host, user, port, key_path}
k8s: {cluster_name, kube_ovn_iface, kube_service_addresses, kube_pods_subnet}
metallb: {external_pool, internal_pool, interface}
ovn: {int_bridge, bridges, ports, mappings, availability_zone}
overrides: {endpoints, neutron, nova, prometheus}
nodes: [{name, ansible_host, node_ip, roles:[controller|compute|network|storage], bridges:{}}]
```

Public hostnames default to `<prefix>.<domain>`. Override per service with
`overrides.endpoints.hosts: {keystone: keystone-user4.example.site, skyline: …}`
— this drives the service catalog (`endpoints.yaml`, grafana, barbican, probes
via `overrides.host()`) **and** the gateway. genestack builds the gateway from
two layers that must agree: the **HTTPRoute** (`custom-<svc>-gateway-route`) and
the **Gateway listener** (`<svc>-https` on `envoy-gateway/flex-gateway`, with a
cert-manager-issued cert). The generated `manifests/gateway/patch-routes.sh`
(step `gw.routes`) swaps the default `<svc>.<domain>` for the override on **both**
(jq-based, preserving other hostnames); cert-manager (annotation-driven on the
Gateway) then reissues the TLS cert for the new name. So DNS for the custom name
must point at the gateway VIP for ACME HTTP01 to succeed. Re-run `overrides
upload` after editing `hosts`. Route/listener names are genestack-specific —
verify with `kubectl get httproute -A` and `kubectl -n envoy-gateway get gateway
flex-gateway -o yaml`. Services without a genestack route (e.g. harbor) are
warned and skipped.

`ansible_host` = SSH/management address (e.g. WAN). `node_ip` = the private
cluster IP kubespray binds k8s/etcd to (→ `ip`/`access_ip`). Editing
`cluster.yaml` does **not** auto-sync to the deployer — re-run `overrides upload`
/ the relevant phase.

## Extending the tool

- **New phase/step**: add to `engine.DefaultPhases()`. IDs are the state keys
  and the `run`/`reset` targets (e.g. `os.nova`). Use placeholders for any
  site-specific value. Mark backend/optional bits `Optional: true`.
- **New generated override**: add a generator in `overrides` and to `Managed()`.
- **New typed knob**: add to `model.Overrides` (+ `fillDefaults`) and reference
  in a generator or via a placeholder.
- **Add a Step that uploads a generated file**: give it an `Action` and handle
  it in `runner.RunStep`.
- Genestack `bin/install-*.sh` scripts often **return 0 even when helm fails** —
  add a post-check (e.g. verify a deployment/CRD exists) so the engine catches it
  (see `gw.install`, `metallb.install`).

## Known gotchas (learned the hard way)

- **kube-ovn `VLAN_INTERFACE_NAME`** = the vlan/underlay provider NIC; kube-ovn
  **bridges it into OVS (br-int)** and the NIC loses L3. For a geneve overlay set
  only `IFACE`; leave `VLAN_INTERFACE_NAME` blank (genestack does provider nets
  via its own OVN). `ovn.config` already does this with anchored seds.
- **group_vars override inventory.yaml**: genestack's seeded
  `/etc/genestack/inventory/group_vars/` hardcode `kube_pods_subnet`
  (10.236.0.0/14) etc., and Ansible loads `inventory/group_vars/*` at higher
  precedence than the inline `vars:` in `inventory.yaml` — so the CLI's
  inventory value is silently ignored. `config.groupvars` writes
  `group_vars/k8s_cluster/zz-genestack-cli.yml` (alphabetically last ⇒ wins) to
  pin the model's pod/service CIDRs. Anything that must beat genestack defaults
  belongs in that override file, not just inventory.yaml.
- **Deployer is also a cluster node**: `ansible all -m reboot` would reboot the
  deployer itself and kill ansible / the control session (`new session: EOF`).
  `host.reboot` excludes `{{DEPLOY_NODE}}`; reboot the deployer last via the
  opt-in `host.reboot.self` (or manually), reconnect, then `run kubernetes`.
- **Re-applying OVN annotations** needs deleting the `ovn.openstack.org/configured`
  **label** so the `ovn-setup` DaemonSet (ns `openstack`) re-runs; just changing
  the annotation isn't enough.
- **OpenStack-Helm secret ownership**: genestack's `kubesecrets.yaml`
  pre-creates secrets that some charts also want to own (e.g. `nova-ssh`,
  `octavia-certificates`) → Helm "invalid ownership metadata" error. Use
  `helmAdopt(release, secrets...)` before the install (see `os.nova`).
- **Cloud NICs**: on cloud providers, secondary NICs may be DHCP and not persist
  across reboot → pin static via netplan. MetalLB L2 VIPs need the cloud's
  **allowed-address-pairs** or ARP is dropped.
- **OrbStack lab on macOS**: Go can't dial OrbStack `IFSCOPE` bridge IPs → drive
  the lab in **local mode inside the deployer** (`scripts/orbstack-lab.sh run …`).

## Roadmap (not yet done)

netplan generation from `node.bridges`; `genestack preflight` (ansible ping /
MTU / DNS / disk); `genestack ovn reconfigure` (re-annotate + clear the
`configured` label); auto `overrides upload` before phases that need them;
`known_hosts` verification; generic detection of install-script helm failures.

## Lab for testing

`scripts/orbstack-lab.sh up|run|down` spins up disposable OrbStack Ubuntu VMs.
On macOS use `scripts/orbstack-lab.sh run <args>` (local mode in the deployer).
