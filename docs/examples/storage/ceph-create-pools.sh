#!/usr/bin/env bash
#
# Create per-tenant RBD pools + cephx keys for a genestack OpenStack deployment.
#
#   pools created:  <prefix>images  <prefix>volumes  <prefix>vms
#   keys created:   client.<prefix>glance   client.<prefix>cinder
#
# The prefix is taken verbatim, so include any separator yourself:
#   ./ceph-create-pools.sh user7-   ->  user7-images / user7-volumes / user7-vms
#   ./ceph-create-pools.sh ""       ->  images / volumes / vms   (no prefix)
#
# Run on a Ceph node with admin access, or wrap it:
#   ssh <mon> cephadm shell -- bash -s -- user7- < ceph-create-pools.sh
#
# Output: the fsid + raw and base64 keyrings, ready to paste into the
# genestack-cli cluster.yaml overrides and the ceph-secrets.yaml Secrets.
set -euo pipefail

PREFIX="${1-}"
if [[ $# -lt 1 ]]; then
  echo "usage: $0 <prefix>            # pools: <prefix>images <prefix>volumes <prefix>vms" >&2
  echo "       $0 user7-              # -> user7-images user7-volumes user7-vms" >&2
  exit 1
fi
command -v ceph >/dev/null || { echo "error: 'ceph' not found — run on a Ceph node or inside 'cephadm shell'" >&2; exit 1; }

IMAGES="${PREFIX}images"
VOLUMES="${PREFIX}volumes"
VMS="${PREFIX}vms"
GLANCE_USER="client.${PREFIX}glance"
CINDER_USER="client.${PREFIX}cinder"

b64() { printf '%s' "$1" | base64 -w0 2>/dev/null || printf '%s' "$1" | base64; }
uuid() { uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid; }

echo "==> creating pools: ${IMAGES} ${VOLUMES} ${VMS}" >&2
for pool in "$IMAGES" "$VOLUMES" "$VMS"; do
  ceph osd pool create "$pool" >/dev/null   # idempotent: no-op if it already exists
  rbd pool init "$pool"
  echo "    ready: ${pool}" >&2
done

echo "==> creating keys: ${GLANCE_USER} ${CINDER_USER}" >&2
GLANCE_KEY=$(ceph auth get-or-create-key "$GLANCE_USER" \
  mon 'profile rbd' \
  osd "allow class-read object_prefix rbd_children, profile rbd pool=${IMAGES}" \
  mgr "profile rbd pool=${IMAGES}")
CINDER_KEY=$(ceph auth get-or-create-key "$CINDER_USER" \
  mon 'profile rbd' \
  osd "profile rbd pool=${VOLUMES}, profile rbd pool=${VMS}, profile rbd-read-only pool=${IMAGES}" \
  mgr "profile rbd pool=${VOLUMES}, profile rbd pool=${VMS}")

FSID=$(ceph fsid)

cat <<EOF

================ genestack-cli values (prefix='${PREFIX}') ================
fsid:           ${FSID}
secret_uuid:    $(uuid)     # use as rbd_secret_uuid (keep ONE per deployment)

pools:          images=${IMAGES}  volumes=${VOLUMES}  vms=${VMS}
glance user:    ${GLANCE_USER#client.}   # -> rbd_store_user
cinder user:    ${CINDER_USER#client.}   # -> rbd_user

# raw keyrings (inline rbd_user_keyring, etc.):
glance_key:     ${GLANCE_KEY}
cinder_key:     ${CINDER_KEY}

# base64 for the Secret data.key fields in ceph-secrets.yaml:
#   images-rbd-keyring   data.key:  ${GLANCE_KEY_B64:=$(b64 "$GLANCE_KEY")}
#   pvc-ceph-client-key  data.key:  ${CINDER_KEY_B64:=$(b64 "$CINDER_KEY")}
==========================================================================
EOF
