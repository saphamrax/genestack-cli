---
title: genestack-cli
---

# genestack-cli

A Go TUI/CLI that deploys OpenStack via
[Genestack](https://github.com/rackerlabs/genestack): it models nodes/roles,
generates the kubespray `inventory.yaml` and the helm override / manifest files,
and runs the install as **resumable phases** over SSH (or locally on the
deployment host).

## Documentation

- **[Deployment guide](DEPLOYMENT.html)** — end-to-end operator guide: prerequisites,
  `cluster.yaml`, the 13-phase pipeline, running phases/steps, post-install.
- **[Storage backends](STORAGE.html)** — Cinder + Glance backends (Ceph, NetApp, …),
  the `ceph.conf` ConfigMap, keyrings, and troubleshooting. Examples:
  [`overrides-cinder-glance.yaml`](examples/storage/overrides-cinder-glance.yaml),
  [`ceph-secrets.yaml`](examples/storage/ceph-secrets.yaml).
- **[Demo](demo/demo.html)** — an asciinema recording of the CLI in action.
- **[Genestack manual](manual-installation.html)** — the upstream manual install this
  tool wraps (source of truth for the real steps).

## Quick start

```bash
make build                                  # -> ./genestack
genestack init                              # create a starter cluster.yaml
genestack validate                          # structural + CIDR checks
genestack phases                            # list the deployment pipeline
genestack run config --to post              # deploy (resumable)
genestack                                   # no args -> launch the TUI
```

See the [README](https://github.com/saphamrax/genestack-cli#readme) and the
[deployment guide](DEPLOYMENT.html) for the full workflow.
