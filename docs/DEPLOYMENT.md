# Deploying OpenStack with genestack-cli — end-to-end guide

This guide takes you from bare Ubuntu hosts to a running Genestack/OpenStack
cluster using `genestack-cli`. It wraps the steps of the Rackspace manual
installation (`docs/manual-installation.html`) into a modelled config, a
generated inventory + override files, and a resumable phase runner.

> **Maturity note.** The CLI orchestration (modelling, inventory/override
> generation, SSH/local execution, resumable phases) is validated through the
> early phases. The full OpenStack service stack has not yet been run end-to-end
> on production hardware via this tool — treat the later phases as "drive +
> verify", and keep the upstream manual handy. See *Known limitations*.

## Contents

1. [Architecture & how it works](#1-architecture--how-it-works)
2. [Prerequisites](#2-prerequisites)
3. [Install the CLI](#3-install-the-cli)
4. [Configure the cluster](#4-configure-the-cluster)
5. [Pre-deploy checks](#5-pre-deploy-checks)
6. [Deploy, phase by phase](#6-deploy-phase-by-phase)
7. [Storage backends (Longhorn / Ceph / NetApp)](#7-storage-backends)
8. [Post-install](#8-post-install)
9. [Resuming, re-running, troubleshooting](#9-resuming-re-running-troubleshooting)
10. [Command reference](#10-command-reference)
11. [cluster.yaml reference](#11-clusteryaml-reference)
12. [Known limitations](#12-known-limitations)

---

## 1. Architecture & how it works

```
ssh mode (from your laptop)              local mode (on the deployment node)
[laptop] ── ssh ──▶ [deployment node]    [deployment node]  (run genestack here)
                      ├ ansible ─▶ targets  ├ ansible ─▶ targets
                      └ kubectl  ─▶ k8s      └ kubectl  ─▶ k8s
```

- The **deployment node** holds `/opt/genestack` and runs all `ansible-playbook`,
  `kubectl` and `bin/install-*.sh` commands. The CLI either drives it over SSH
  (`mode: ssh`, default) or runs directly on it (`mode: local` / `--local`).
- The CLI turns your `cluster.yaml` (nodes + roles + parameters) into:
  - `inventory.yaml` (kubespray/genestack inventory),
  - a set of helm-override / manifest files (endpoints, MetalLB pools, neutron
    MTU, …),
  and uploads them to `/etc/genestack/…`.
- Deployment is a sequence of **phases**, each a list of **steps**. Progress is
  persisted, so a run resumes and skips completed steps.

---

## 2. Prerequisites

### 2.1 Hosts

- **≥ 1 deployment node** + your target nodes. The deployment node can be one of
  the controllers, but a dedicated small node keeps things clean.
- Target nodes: at least **1 controller**; a realistic cluster uses **3
  controllers + N computes**. Production sizing follows the upstream manual.
- OS: **Ubuntu 24.04 LTS** on every host.
- The deployment node needs outbound internet (git clone, image pulls) and `git`
  + `curl` (the `config` phase installs the rest via `bootstrap.sh`).

### 2.2 SSH

- The deployment node must reach every target as **root over SSH with key auth**
  (genestack/ansible assume root). Put the deployment node's public key in each
  target's `/root/.ssh/authorized_keys`.
- If you drive from a laptop (ssh mode), your laptop must reach the deployment
  node over SSH. Prefer a dedicated key and set `deployment.key_path` — the CLI
  then uses that key exclusively (avoids agent keys exhausting `MaxAuthTries`).

### 2.3 Network (target nodes)

Configure netplan on each target with the bridges the manual expects (over a
bonded interface + VLANs). Names and MTUs matter:

| Bridge | MTU | Purpose |
|--------|-----|---------|
| `br-host` | 1500 | host / default gateway; MetalLB advertises VIPs here |
| `br-overlay` | 9000 | Kube-OVN / tenant overlay (`kube_ovn_iface`) |
| `br-storage` | 9000 | storage traffic |
| `br-repl` | 9000 | storage replication |
| `br-pxe` | 1500 | Ironic / PXE |

Validate MTUs across nodes (`ping -c2 -s 9000 <br-overlay IP>` etc.) before
deploying.

### 2.4 MetalLB IP pools

Reserve real, routable IPs on the `br-host` network:

- **external pool** — the gateway VIP that the OpenStack API domain resolves to
  (e.g. `10.230.0.10/32`).
- **internal/primary pool** — internal service VIPs (e.g. `10.230.0.128/25`).

### 2.5 DNS & TLS

- Point the API domain at the external VIP: a wildcard `*.<domain>` → external
  pool VIP (e.g. `*.api.openstack.example.com → 10.230.0.10`).
- The default install uses **Let's Encrypt HTTP-01**, so TCP **80 and 443** on
  the gateway VIP must be reachable from the internet. Otherwise install a static
  wildcard certificate (see the manual's *Static SSL* section) and supply it via
  a passthrough override.

### 2.6 OVN provider network

Know the physical interface used for the provider (external) network on the
network nodes — it goes into `ovn.ports` as `br-ex:<bond>` (e.g. `br-ex:bond1`).

---

## 3. Install the CLI

Build from source (Go ≥ 1.24), or copy a prebuilt static binary to the host.

```bash
# build for your machine
make build                     # -> ./genestack

# or cross-compile static binaries for all platforms into dist/
make build-all
scp dist/genestack-linux-amd64 root@deployer:/usr/local/bin/genestack
```

(`make build` wraps `go build`; `make help` lists all targets. Plain
`go build -o genestack ./cmd/genestack` works too.)

The linux binary is statically linked — no dependencies on the target host.

---

## 4. Configure the cluster

```bash
genestack init                 # writes a starter cluster.yaml
$EDITOR cluster.yaml
```

Set, at minimum:

```yaml
name: prod
region: RegionOne
domain: api.openstack.example.com    # drives every <service>.<domain> public host

deployment:
  mode: ssh                          # or "local" to run on the deployment node
  host: 10.0.0.10                    # deployment node (ssh mode)
  user: root
  port: 22
  key_path: /home/me/.ssh/genestack  # dedicated key (recommended)

k8s:
  cluster_name: cluster.local
  kube_ovn_iface: br-overlay
  kube_service_addresses: 10.233.0.0/18
  kube_pods_subnet: 10.236.0.0/14

metallb:
  external_pool: 10.230.0.10/32
  internal_pool: 10.230.0.128/25
  interface: br-host

ovn:
  bridges: br-ex,br-pxe
  ports: br-ex:bond1                 # your physical provider bond
  mappings: physnet1:br-ex
  availability_zone: az1

overrides:
  neutron:   {global_physnet_mtu: 9000, path_mtu: 1558, physnet1_mtu: 1500}
  nova:      {volume_use_multipath: true, enable_iscsi: true}
  prometheus:{storage: 50Gi}

nodes:
  - {name: ctl01, ansible_host: 10.0.0.51, roles: [controller]}
  - {name: ctl02, ansible_host: 10.0.0.52, roles: [controller]}
  - {name: ctl03, ansible_host: 10.0.0.53, roles: [controller]}
  - {name: cmp01, ansible_host: 10.0.0.54, roles: [compute]}
  - {name: cmp02, ansible_host: 10.0.0.55, roles: [compute]}
```

You can also manage nodes from the CLI:

```bash
genestack node add cmp03 --ip 10.0.0.56 --roles compute
genestack node list
genestack node rm cmp03
```

**Roles → inventory groups:**

| Role | Groups |
|------|--------|
| `controller` | kube_control_plane, etcd, kube_node, openstack_control_plane, ovn_network_nodes, cinder_storage_nodes |
| `compute` | kube_node, openstack_compute_nodes |
| `network` | kube_node, ovn_network_nodes |
| `storage` | kube_node, cinder_storage_nodes |

Every node is always a `kube_node`.

### Override files

The CLI generates these from the model (domain-derived + typed knobs):

| File | Driven by |
|------|-----------|
| `helm-configs/global_overrides/endpoints.yaml` | `domain`, `region` (every `<svc>.<domain>`) |
| `manifests/metallb/metallb-openstack-service-lb.yml` | `metallb.*` |
| `helm-configs/neutron/neutron-helm-overrides.yaml` | `overrides.neutron` |
| `helm-configs/nova/nova-helm-overrides.yaml` | `overrides.nova` |
| `helm-configs/kube-prometheus-stack/prometheus-helm-overrides.yaml` | `overrides.prometheus` |
| `helm-configs/grafana/grafana-helm-overrides.yaml` | `domain` |
| `helm-configs/prometheus-blackbox-exporter/probe_targets.yaml` | `domain` |
| `helm-configs/barbican/barbican-helm-overrides.yaml` | `domain` |

Anything else (Ceph/NetApp/S3 secrets, custom tuning) goes in a local
**`overrides/`** directory that mirrors the remote layout, e.g.
`overrides/helm-configs/cinder/cinder.yaml` →
`/etc/genestack/helm-configs/cinder/cinder.yaml`. Passthrough files **override**
generated ones on a path clash.

```bash
genestack overrides list                  # generated + passthrough, with source
genestack overrides show endpoints.yaml    # preview a rendered file
genestack overrides generate               # write generated files locally to review
```

---

## 5. Pre-deploy checks

```bash
genestack connect                 # verify SSH / local execution works
genestack inventory               # review the generated inventory.yaml
genestack overrides list          # review the files that will be uploaded
genestack run --all --dry-run     # print every command, in order, run nothing
```

`--dry-run` is safe and connects to nothing — use it to sanity-check the full
sequence and the resolved values (domains, OVN ports, MetalLB pools).

---

## 6. Deploy, phase by phase

Drive the whole thing, or one phase at a time. State is persisted, so a re-run
resumes and skips completed steps.

```bash
genestack run --all               # everything, resuming
# or step through:
genestack run config
genestack run host-setup
genestack run kubernetes
...
genestack status                  # progress summary
genestack steps <phase>           # per-step status
```

You can also use the TUI (`genestack`), pressing `enter` to run the selected
phase and watching logs stream live.

The phases, in order:

| # | Phase | What it does | Verify |
|---|-------|--------------|--------|
| 1 | `config` | clone `/opt/genestack`, `bootstrap.sh`, upload inventory + overrides | `kubectl version --client` not yet; check `/opt/genestack` + `/etc/genestack/inventory/inventory.yaml` exist |
| 2 | `host-setup` | `host-setup.yml`, configure playbooks, dist-upgrade, **reboot all targets** | `ansible all -m ping` returns pong for all |
| 3 | `kubernetes` | kubespray `cluster.yml` (~15–20 min), label/untaint nodes | `kubectl get nodes` all Ready; labels present |
| 4 | `kube-ovn` | copy overrides, label, `install-kube-ovn.sh` | `kubectl get pods -A` ovn pods Running; `kubectl get subnets.kubeovn.io` |
| 5 | `metallb` | namespace, `install-metallb.sh`, apply pools | `kubectl -n metallb-system get deploy metallb-controller` 1/1 |
| 6 | `openstack-ns` | create namespace, `create-secrets.sh`, apply secrets | `kubectl -n openstack get secrets` |
| 7 | `longhorn` | label, namespace, `install-longhorn.sh`, storage classes | `kubectl -n longhorn-system get pods`; `kubectl get sc` |
| 8 | `gateway` | `install-envoy-gateway.sh`, wait, `setup-envoy-gateway.sh` | `kubectl -n envoy-gateway get gateways` flex-gateway has VIP |
| 9 | `infra` | memcached, libvirt, MariaDB operator+cluster, RabbitMQ operator+cluster | `kubectl -n openstack get mariadbs,rabbitmqclusters` ready |
| 10 | `ovn-config` | annotate bridges/ports/mappings/AZ, apply OVN | nodes have `ovn.openstack.org/configured` label |
| 11 | `openstack` | keystone → glance → cinder → placement → nova → neutron → barbican → skyline → heat (octavia optional) | `kubectl -n openstack exec -ti openstack-admin-client -- openstack endpoint list` |
| 12 | `post` | list endpoints; (optional) configure glance/nova/neutron via openstack-ops | `openstack service list` |

**Optional steps** (`config.opsbootstrap`, `openstack.octavia`, `post.images`)
are skipped on a phase / `--all` run. Run them by naming the step directly or
with `--optional`:

```bash
genestack run os.octavia          # run one optional step
genestack run openstack --optional
```

> Always wait for the previous phase's pods to be Ready before starting the next
> (the table's *Verify* column). Kube-OVN, MetalLB, the databases and the
> gateway must be healthy before the OpenStack services land.

---

## 7. Storage backends

> Full guide with Ceph + NetApp end-to-end, `ceph.conf` ConfigMap, keyrings and
> troubleshooting: **[STORAGE.md](STORAGE.md)** (examples in
> [`examples/storage/`](examples/storage/)).

Cinder needs a backend; Glance can also use Ceph for its image store.

`overrides.cinder` and `overrides.glance` are **free-form** — whatever YAML you put
there is rendered verbatim into `helm-configs/{cinder,glance}/<svc>-helm-overrides.yaml`,
which genestack's install scripts pick up. Storage backends (Ceph, NetApp, Pure, …)
are therefore pure config: a new backend needs **no CLI changes**, you just add its
block. Multiple Cinder backends run together via `enabled_backends`.

```yaml
overrides:
  glance:                      # -> helm-configs/glance/glance-helm-overrides.yaml
    storage: rbd
    conf:
      glance:
        glance_store:
          default_backend: rbd
          rbd: {rbd_store_pool: images, rbd_store_user: glance, rbd_store_ceph_conf: /etc/ceph/ceph.conf}
  cinder:                      # -> helm-configs/cinder/cinder-helm-overrides.yaml
    storage: ceph
    conf:
      cinder:
        DEFAULT:
          enabled_backends: rbd-ceph,netapp-iscsi-block1
          default_volume_type: rbd-ceph
        backends:
          rbd-ceph:
            volume_driver: cinder.volume.drivers.rbd.RBDDriver
            rbd_pool: volumes
            rbd_secret_uuid: <uuidgen>
          netapp-iscsi-block1:
            volume_driver: cinder.volume.drivers.netapp.common.NetAppDriver
            netapp_login: <vserver admin>
            netapp_password: <vserver password>   # secret — cluster.yaml is gitignored
            netapp_server_hostname: openstack-block1
            netapp_vserver: <vserver name>
```

Backend **secrets/ConfigMaps** that aren't helm values (e.g. the Ceph `ceph-etc`
ConfigMap and RBD keyring Secrets, per the manual's *Ceph RBD External Cluster*
section) go as passthrough manifests under `overrides/manifests/storage/`. The
`os.storage` step applies that directory (`kubectl apply -f`) before Glance/Cinder.
Any backend-specific host prep (e.g. trusting a NetApp vserver cert) is a one-off
operator step per the manual.

> **Secrets:** keyrings / passwords live in `cluster.yaml` (or the passthrough
> manifests), which are gitignored — keep them out of source control.

Upload + apply:

```bash
genestack overrides upload
genestack run os.storage os.glance os.cinder --force
```

---

## 8. Post-install

After the `openstack` phase is healthy:

```bash
# admin client shell
kubectl -n openstack exec -ti openstack-admin-client -- bash

# quotas, images, flavors (or use the openstack-ops playbooks: post.images step)
openstack image list
openstack flavor list
```

Then per the manual:

- set quotas,
- create projects/users (`openstack project create`, `openstack user create`),
- create external + tenant networks and a router, allocate a floating IP,
- boot a test VM and verify connectivity,
- log in to the dashboard at `https://skyline.<domain>` (admin password from
  `~/.config/openstack/clouds.yaml` on the deployment node).

---

## 9. Resuming, re-running, troubleshooting

- **Resume:** just re-run `genestack run --all` (or the phase). Completed steps
  are skipped.
- **Re-run a step/phase:** clear its state, or force.
  ```bash
  genestack reset kubernetes        # re-arm a phase
  genestack run k8s.cluster --force # re-run even if done
  ```
- **Inspect:** `genestack status`, `genestack steps <phase>`. State lives in
  `.<config>.state.json` next to your `cluster.yaml`.
- **Preview a step's exact command:** `genestack run <step> --dry-run`.

Common issues:

- **`ssh: unable to authenticate … MaxAuthTries`** — your ssh-agent offered too
  many keys. Set `deployment.key_path` (the CLI then uses only that key).
- **A step fails mid-phase** — the run stops at that step (status `failed`). Fix
  the underlying cause on the host, then re-run the phase (it resumes from the
  failed step).
- **Pods Pending / CrashLoopBackOff** — almost always a missing prerequisite from
  an earlier phase (CNI not up, no storage class, DB not ready). Check the
  *Verify* column for the previous phase.
- **`no route to host` from a Go binary against OrbStack IPs on macOS** — a
  host/OrbStack `IFSCOPE` quirk, not a real-host issue. Use local mode in the lab
  (see the [README](../README.md) lab section).

---

## 10. Command reference

```
Global flags:  --config <path>   (default cluster.yaml; or $GENESTACK_CONFIG)
               --local           run on this host instead of over SSH

Setup:
  init                                   create a starter cluster.yaml
  node add NAME --ip IP --roles r1,r2    add a node
  node list | node rm NAME
  inventory [--upload] [--output FILE]   print / write / upload inventory.yaml
  overrides list | show PATH | generate [--output-dir DIR] | upload
  connect                                test connectivity

Deploy:
  phases                                 list phases + status
  steps [PHASE]                          list steps + status
  status                                 progress summary
  run TARGET... [--all --from P --to P --force --optional --dry-run]
  reset TARGET... [--all --from P --to P]
  tui                                    launch the terminal UI

TARGET = phase id (e.g. kubernetes) or step id (e.g. k8s.cluster).
```

---

## 11. cluster.yaml reference

```yaml
name: string
region: string                 # e.g. RegionOne
domain: string                 # e.g. api.openstack.example.com
ansible_port: int              # default 22

deployment:
  mode: ssh | local            # default ssh
  host: string                 # deployment node (ssh mode)
  user: string                 # default root
  port: int                    # default 22
  key_path: string             # optional; used exclusively when set

k8s:
  cluster_name: string         # cluster.local
  kube_ovn_iface: string       # br-overlay
  kube_service_addresses: cidr # 10.233.0.0/18
  kube_pods_subnet: cidr       # 10.236.0.0/14
  upstream_dns_servers: [ip]   # optional

metallb:
  external_pool: cidr          # gateway VIP, e.g. 10.230.0.10/32
  internal_pool: cidr          # e.g. 10.230.0.128/25
  interface: string            # br-host

ovn:
  int_bridge: string           # br-int
  bridges: string              # br-ex,br-pxe
  ports: string                # br-ex:bond1
  mappings: string             # physnet1:br-ex
  availability_zone: string    # az1

overrides:
  endpoints:  {scheme: https, port: 443}
  neutron:    {global_physnet_mtu: 9000, path_mtu: 1558, physnet1_mtu: 1500}
  nova:       {volume_use_multipath: true, enable_iscsi: true}
  prometheus: {storage: 50Gi}

nodes:
  - name: string               # inventory_hostname
    ansible_host: ip            # reached over SSH (e.g. WAN)
    node_ip: ip                 # optional; private cluster IP -> kubespray ip/access_ip
    roles: [controller|compute|network|storage]
    bridges: {br-host: ip, ...}  # optional, informational
```

Defaults are filled in for omitted fields, so a minimal config still renders.

---

## 12. Known limitations

- **Not yet run e2e on production hardware via this tool.** The phases mirror the
  manual, but the OpenStack service phases (10–12) should be driven with care and
  verified against the upstream manual.
- **No per-host netplan generation.** `node.bridges` is informational; configure
  netplan on the hosts yourself (section 2.3).
- **No `known_hosts` verification.** SSH trusts the host key on first use (lab
  default). Harden for production if needed.
- **Backends/secrets are passthrough**, not modelled — Ceph/NetApp/S3 require
  hand-authored override files (section 7).
- **Verification is manual** between phases (the *Verify* column); the runner does
  not yet poll readiness for every install step.

For trying the flow on disposable machines, see the OrbStack lab in the
[README](../README.md#try-it-on-a-throwaway-orbstack-lab).
```
