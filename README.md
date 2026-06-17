# genestack-cli

A terminal UI / CLI to deploy an OpenStack cluster with
[Genestack](https://github.com/rackerlabs/genestack), following the Rackspace
manual installation guide (`docs/manual-installation.html`).

It lets you model nodes and roles, generate the kubespray/genestack
`inventory.yaml`, and drive the deployment **phase by phase over SSH** to the
deployment (jump) host — without hand-running playbooks or SSHing into each
target node.

📖 **Full end-to-end deployment guide: [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)**
(prerequisites → config → per-phase deploy → post-install → troubleshooting).

## How it works

Two execution modes, same commands and TUI:

```
ssh mode (default)                 local mode
[your laptop]                      [deployment node  /opt/genestack]
  genestack (TUI/CLI)                genestack (TUI/CLI)
    ── ssh ──▶ [deployment node]       ├─ ansible-playbook ─▶ target nodes
                 ├─ ansible ─▶ nodes   └─ kubectl / bin/install-*.sh ─▶ k8s
                 └─ kubectl ─▶ k8s
```

Pick a mode with `deployment.mode: local` in the config, or the global
`--local` flag (`genestack --local run --all`). In `local` mode the SSH fields
are ignored and commands run on the current host via `bash -c`.

- **Model** (`internal/model`): `Cluster`, `Node`, `Role`, persisted to `cluster.yaml`.
- **Inventory** (`internal/inventory`): renders the exact `inventory.yaml` from roles.
- **Overrides** (`internal/overrides`): renders genestack helm-override files from
  the model and merges them with operator-authored passthrough files.
- **Engine** (`internal/engine`): the ordered deployment phases + per-step run
  state (resumable, persisted to `.<config>.state.json`).
- **Exec** (`internal/exec`): an `Executor` interface with two backends — SSH
  (remote) and local (`bash -c`) — both streaming command output back live.
- **TUI** (`internal/tui`): Bubble Tea dashboard (nodes / phases / logs).

### Role → inventory group mapping

| Role         | Groups |
|--------------|--------|
| `controller` | kube_control_plane, etcd, kube_node, openstack_control_plane, ovn_network_nodes, cinder_storage_nodes |
| `compute`    | kube_node, openstack_compute_nodes |
| `network`    | kube_node, ovn_network_nodes |
| `storage`    | kube_node, cinder_storage_nodes |

Every node is always a `kube_node` (it must join Kubernetes to run pods).

## Quick start

```bash
make build          # -> ./genestack   (or: go build -o genestack ./cmd/genestack)
make build-all      # cross-compile into dist/ (linux+darwin, amd64+arm64)
make help           # list all make targets

./genestack init                                  # writes a sample cluster.yaml
$EDITOR cluster.yaml                              # set deployment.host, region, domain
./genestack node add cmp02 --ip 10.0.0.55 --roles compute
./genestack inventory                             # preview generated inventory.yaml
./genestack                                       # launch the TUI (default command)
```

### TUI keys

| Key | Action |
|-----|--------|
| `tab` | switch focus (nodes ↔ phases) |
| `↑/↓` `k/j` | navigate |
| `c` | connect SSH to the deployment host |
| `g` | generate **and upload** inventory to `/etc/genestack/inventory/inventory.yaml` |
| `a` / `d` | add / delete a node |
| `enter` | run the selected phase |
| `R` | run all phases from the selected one onward (resumes, skips done) |
| `pgup/pgdown` | scroll logs |
| `q` | quit |

Optional steps (Ceph, NetApp, Octavia, image upload) are **skipped** on a bulk
`R` run and must be triggered deliberately.

## CLI commands

Setup:

```
genestack init                 create a starter cluster.yaml
genestack node add NAME --ip IP --roles controller[,compute]
genestack node list
genestack node rm NAME
genestack inventory [--upload] [--output FILE]   print / write / upload inventory.yaml
genestack connect              test SSH to the deployment host
genestack tui                  launch the terminal UI (default)
```

Scaling (add compute nodes to a **running** cluster):

```
genestack scale add NAME --ip IP [--node-ip IP] [--roles compute]   register in cluster.yaml
genestack scale apply NAME [NAME...]                                onboard those nodes
```

`scale apply` re-uploads the inventory, joins the new hosts with kubespray
`scale.yml` (limited to them), runs host-setup, then labels and OVN-annotates only
the new nodes so nova/neutron pick up their agents. Worker nodes only (controller
scaling is unsupported). Flags: `--dry-run`, `--optional`, `--skip-os-update`,
`--from STEP` (resume), `--no-log`.

`--config <path>` or `$GENESTACK_CONFIG` selects the config file.

### Override files (domains & service parameters)

Genestack reads helm-override / config files from `/etc/genestack/helm-configs/…`.
The CLI manages these in three layers:

1. **Generated** from the model — domain-derived files (`endpoints.yaml` with all
   ~25 service hosts, blackbox `probe_targets.yaml`, grafana `custom_host`,
   barbican host) plus typed knobs (neutron MTU, nova multipath, prometheus PV).
   These come from `domain`, `region` and the `overrides:` block in `cluster.yaml`.
2. **Passthrough** — any file you drop under the local `overrides/` directory
   (mirroring the remote layout, e.g. `overrides/helm-configs/cinder/cinder.yaml`)
   is uploaded verbatim. Use this for NetApp/Ceph/S3 backends, secrets, and
   anything not modelled. On a path clash, **passthrough wins over generated**.
3. **Upload** — push the merged set to the deployment host.

```
genestack overrides list                  # show planned files + their source
genestack overrides show endpoints.yaml   # preview a rendered file
genestack overrides generate              # write generated files locally to review
genestack overrides upload                # push merged files to the host
```

Tune the modelled knobs in `cluster.yaml`:

```yaml
domain: api.openstack.example.com   # drives every <service>.<domain> host
region: RegionOne
genestack_version: release-2025.4   # genestack git ref to checkout (optional)
metallb:                            # MetalLB IP pools (manifests/metallb/...)
  external_pool: 10.230.0.10/32
  internal_pool: 10.230.0.128/25
  interface: br-host
ovn:                                # OVN node annotations (site/hardware specific)
  int_bridge: br-int
  bridges: br-ex,br-pxe
  ports: br-ex:bond1                # physical bond per node
  mappings: physnet1:br-ex
  availability_zone: az1
overrides:
  endpoints: {scheme: https, port: 443}
  neutron:   {global_physnet_mtu: 9000, path_mtu: 1558, physnet1_mtu: 1500}
  nova:      {volume_use_multipath: true, enable_iscsi: true}
  prometheus:{storage: 50Gi}
```

In the TUI, `o` generates and uploads overrides (like `g` for inventory).

The `config` phase also includes `config.inventory` and `config.overrides`
steps, so a `run` deployment uploads the generated inventory and override files
(after `bootstrap.sh` has created `/etc/genestack`) without a separate manual
step. The MetalLB pool file is part of the generated overrides, so the metallb
phase has a file to apply.

### Step-by-step deployment (no TUI)

Run the deployment from the command line — useful for scripting, CI, or doing
the install one controlled step at a time. Progress is persisted to
`.<config>.state.json` so runs resume and skip completed steps.

```
genestack phases               list phases with status
genestack steps [PHASE]        list steps (optionally for one phase) with status
genestack status               summarise progress
genestack run TARGET... [flags]
genestack reset TARGET... [flags]
```

`TARGET` is a **phase id** (e.g. `kubernetes`) or a **step id** (e.g.
`k8s.cluster`); list them with `genestack steps`. Flags for `run`/`reset`:
`--all`, `--from PHASE`, `--to PHASE`; `run` also takes `--force` (re-run done
steps), `--optional` (include optional steps), `--dry-run` and
`--genestack-version REF` (override the genestack git ref to checkout, e.g.
`release-2025.4`; otherwise from `cluster.yaml`'s `genestack_version`).

```bash
genestack run --all                       # whole deployment, resumes, skips done
genestack run config host-setup           # two phases in order
genestack run kubernetes --from kube-ovn  # from one phase to the end
genestack run --from kube-ovn --to metallb
genestack run k8s.cluster                 # a single step
genestack run os.octavia                  # an optional step (run by naming it)
genestack run kubernetes --dry-run        # preview the exact remote commands
genestack reset kubernetes                # re-arm a phase to run again
```

Optional steps (Ceph, NetApp, Octavia, image upload) are skipped during phase
or `--all` runs unless you name the step directly or pass `--optional`.

Every `run` (and the TUI) writes logs to `<config dir>/logs/<timestamp>/`: one
`<step-id>.log` per step plus a combined `run.log` — captured even when a step
fails. Change the location with `--log-dir DIR`, or disable with `--no-log`.

## Try it on a throwaway OrbStack lab

`scripts/orbstack-lab.sh` spins up real (lightweight) Ubuntu machines to exercise
the CLI end-to-end:

```bash
scripts/orbstack-lab.sh up          # deployer + 1 controller + 1 compute (configurable)
scripts/orbstack-lab.sh run run config --dry-run
scripts/orbstack-lab.sh run run config        # clone + bootstrap + upload inventory/overrides
scripts/orbstack-lab.sh down        # delete the machines
```

Topology via env: `GS_CONTROLLERS`, `GS_COMPUTES`, `GS_DISTRO`, `GS_ARCH`.

**macOS note:** use the script's `run` subcommand (genestack in **local mode**
inside the deployer). Go's net stack cannot dial OrbStack's `IFSCOPE`'d bridge
IPs from macOS, so SSH mode *from the Mac* fails with "no route to host" even
though plain `ssh` works — a host/OrbStack quirk, not a deployment-host issue.
The generated `lab/cluster.yaml` (SSH mode) is correct for a normal routable host.

**Lab ceiling:** the lab validates the CLI orchestration through `config` and
into `host-setup`, but `host-setup` stops at *Load kernel module(s)* because
OrbStack's minimal kernel does not ship some modules (e.g. `dm_multipath`).
That is an OrbStack limitation, not a CLI issue — real Ubuntu hosts have these
modules. Use the lab to exercise config/inventory/overrides/ansible wiring; use
real (or amd64) hosts to go further.

## Status / roadmap

This is an early scaffold. Working today: node/role modelling, inventory
generation, SSH-streamed phase execution with resume, the TUI.

Not yet implemented (PRs welcome):

- netplan generation per node from `bridges` (currently informational only)
- `known_hosts` verification (SSH currently trusts the host key on first use)
- inline editing of cluster-wide vars (region/domain/CIDRs) from the TUI
- MTU / connectivity pre-flight checks across bridges
- richer per-step output capture (exit codes surfaced inline)
```
