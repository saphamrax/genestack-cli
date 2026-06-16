# Storage backends (Cinder volumes + Glance images)

genestack-cli keeps **Cinder** and **Glance** storage config *backend-agnostic*.
`overrides.cinder` and `overrides.glance` in `cluster.yaml` are **free-form YAML**
rendered verbatim into the helm-override files that genestack's install scripts
consume:

| `cluster.yaml` key   | generated file (on the deploy host)                               | consumed by          |
|----------------------|------------------------------------------------------------------|----------------------|
| `overrides.cinder`   | `/etc/genestack/helm-configs/cinder/cinder-helm-overrides.yaml`  | `install-cinder.sh`  |
| `overrides.glance`   | `/etc/genestack/helm-configs/glance/glance-helm-overrides.yaml`  | `install-glance.sh`  |

A new backend (Ceph, NetApp, Pure, LVM, …) is therefore **pure config** — add a
block under `conf.cinder.backends` and list its name in `enabled_backends`. No CLI
code changes.

> `install-cinder.sh` / `install-glance.sh` include **every** `*.yaml` under
> `helm-configs/<svc>/` as a helm `-f` values file, so the generated override is
> merged on top of genestack's base values.

Example files:
- [`examples/storage/overrides-cinder-glance.yaml`](examples/storage/overrides-cinder-glance.yaml) — the `overrides:` block (Ceph + NetApp)
- [`examples/storage/ceph-secrets.yaml`](examples/storage/ceph-secrets.yaml) — the `ceph-etc` ConfigMap + RBD keyring Secrets

---

## Anatomy of a backend

Cinder deploys a `cinder-volume` pod **only when** there is at least one backend in
`enabled_backends` **and** `manifests.deployment_volume: true`. Both default to
empty/false, which is why a stock install has only `cinder-api` + `cinder-scheduler`.

```yaml
overrides:
  cinder:
    conf:
      cinder:
        DEFAULT:
          enabled_backends: rbd-ceph,netapp-iscsi-block1   # comma-separated, one per backend
          default_volume_type: rbd-ceph                    # which one new volumes use
        backends:
          rbd-ceph:            { volume_driver: ...RBDDriver, ... }
          netapp-iscsi-block1: { volume_driver: ...NetAppDriver, ... }
    manifests:
      deployment_volume: true                              # REQUIRED to get the pod
```

---

## Ceph (external) — Glance image store + Cinder `rbd-ceph`

### 1. Create pools + keys on the Ceph cluster (use the **br-storage** monitor IPs)

Use the helper [`examples/storage/ceph-create-pools.sh`](examples/storage/ceph-create-pools.sh).
It creates `<prefix>images`, `<prefix>volumes`, `<prefix>vms` pools and the
matching `client.<prefix>glance` / `client.<prefix>cinder` keys (scoped to those
pools), then prints the `fsid`, a `secret_uuid`, and the raw + base64 keyrings —
ready to paste into `cluster.yaml` and `ceph-secrets.yaml`.

```bash
# run on a Ceph node (or pipe into cephadm shell). Prefix is verbatim — include
# any separator. Empty prefix -> images/volumes/vms.
ssh <mon> cephadm shell -- bash -s -- user7-  < docs/examples/storage/ceph-create-pools.sh
```

Take `images=<prefix>images`, `volumes=<prefix>volumes`, `vms=<prefix>vms`, the two
keyrings (base64) and the `fsid`/`secret_uuid` from its output into the steps below.

### 2. Cinder/Glance overrides

Copy the `cinder:`/`glance:` blocks from
[`examples/storage/overrides-cinder-glance.yaml`](examples/storage/overrides-cinder-glance.yaml)
into your `cluster.yaml` under `overrides:`. Set `rbd_secret_uuid` to `$UUID`.

### 3. Ceph `ceph.conf` + keyrings as a passthrough manifest

The `ceph.conf` is delivered as the **`ceph-etc` ConfigMap** (mounted at
`/etc/ceph/ceph.conf`), and the keyrings as Secrets. These are not helm values, so
they are passthrough manifests:

```bash
mkdir -p overrides/manifests/storage
cp docs/examples/storage/ceph-secrets.yaml overrides/manifests/storage/ceph-secrets.yaml
# fill in: fsid ($FSID), mon_host (br-storage IPs), and the two base64 keyrings
```

In the ConfigMap, `ceph.conf` uses a YAML **literal block scalar** (`|`) — the
indented lines below it become the file content verbatim:

```yaml
data:
  ceph.conf: |
    [global]
    fsid = <fsid>
    mon_host = [v2:IP1:3300/0,v1:IP1:6789/0] [v2:IP2:3300/0,v1:IP2:6789/0] ...
```

### 4. Apply

```bash
genestack --config cluster.yaml overrides upload          # helm overrides + passthrough manifests
genestack --config cluster.yaml run os.storage --force    # kubectl apply -f manifests/storage/
genestack --config cluster.yaml run os.glance --force
genestack --config cluster.yaml run os.cinder --force     # helm upgrade -> deploys cinder-volume
```

> **Attaching Ceph volumes to VMs** also needs the libvirt/nova RBD secret
> (`rbd_secret_uuid` + a libvirt override). That's out of the current Cinder/Glance
> scope — create volumes works; attach-to-instance needs that extra wiring.

---

## NetApp ONTAP iSCSI — additional Cinder backend

Add the `netapp-iscsi-block1` block (see the example) to `conf.cinder.backends` and
to `enabled_backends`. NetApp prerequisites (per the manual): data LIFs with iSCSI
enabled, MTU 9000 on the storage VLAN. One-off host prep — trust the vserver TLS
cert on the control-plane nodes:

```bash
source /opt/genestack/scripts/genestack.rc
ansible openstack_control_plane -m lineinfile \
  -a "path=/etc/hosts line='172.24.200.25 openstack-block1'"
openssl s_client -connect openstack-block1:443 -showcerts </dev/null \
  | awk '/BEGIN/,/END/{print > "/etc/ssl/certs/netapp-vserver.pem"}' && update-ca-certificates --fresh
ansible openstack_control_plane -m synchronize -a 'src=/etc/ssl/certs/netapp-vserver.pem dest=/etc/ssl/certs/netapp-vserver.pem'
ansible openstack_control_plane -m command -a 'update-ca-certificates --fresh'
```

---

## Verify

```bash
kubectl -n openstack get pods | grep cinder-volume        # should be Running
kubectl -n openstack exec -ti openstack-admin-client -- openstack volume service list
#   cinder-volume  <node>@rbd-ceph            enabled  up
#   cinder-volume  <node>@netapp-iscsi-block1 enabled  up
kubectl -n openstack exec -ti openstack-admin-client -- openstack volume type list
```

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Only `cinder-api` + `cinder-scheduler`, no `cinder-volume` | No backend uploaded, or `manifests.deployment_volume` not `true`. Check `/etc/genestack/helm-configs/cinder/` is non-empty and re-run `overrides upload` + `os.cinder`. |
| `cinder-volume` CrashLoops | Missing `ceph-etc` ConfigMap / `pvc-ceph-client-key` Secret, or `rbd_secret_uuid` mismatch. `kubectl -n openstack logs <pod>`. |
| backend `down` in `volume service list` | Backend unreachable: Ceph mons not reachable on br-storage, or NetApp vserver/TLS not trusted. |
| helm override not taking effect | It must be under `helm-configs/cinder/` **before** `install-cinder.sh` runs; the install merges every `*.yaml` there. |
