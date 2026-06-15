#!/usr/bin/env bash
#
# orbstack-lab.sh — spin up a throwaway OrbStack lab to exercise genestack-cli
# against real (lightweight) Ubuntu machines.
#
# It creates a deployment host plus controller/compute target nodes, installs an
# SSH server and a shared key on each, and writes a ready-to-use cluster.yaml
# pointing at them. You then drive the deployment with the genestack CLI in SSH
# mode from your Mac, or in local mode from inside the deployment host.
#
# Requirements: OrbStack (orb/orbctl), and Go if you want the local-mode binary.
#
# Usage:
#   scripts/orbstack-lab.sh up        # create + provision the lab (default)
#   scripts/orbstack-lab.sh status    # list lab machines + IPs
#   scripts/orbstack-lab.sh ssh       # ssh into the deployment host
#   scripts/orbstack-lab.sh down      # delete all lab machines
#
# Configuration (environment variables):
#   GS_PREFIX       machine name prefix          (default: gs)
#   GS_CONTROLLERS  number of controller nodes   (default: 1)
#   GS_COMPUTES     number of compute nodes      (default: 1)
#   GS_DISTRO       distro:version               (default: ubuntu:24.04)
#   GS_ARCH         arm64|amd64 (empty = native) (default: native)
#   GS_LAB_DIR      where to write key+config    (default: ./lab)
#   GS_DOMAIN       OpenStack API domain         (default: api.lab.local)
#
set -euo pipefail

PREFIX="${GS_PREFIX:-gs}"
CTLS="${GS_CONTROLLERS:-1}"
CMPS="${GS_COMPUTES:-1}"
DISTRO="${GS_DISTRO:-ubuntu:24.04}"
ARCH="${GS_ARCH:-}"
LAB_DIR="${GS_LAB_DIR:-$PWD/lab}"
DOMAIN="${GS_DOMAIN:-api.lab.local}"

DEPLOY="${PREFIX}-deploy"
KEY="$LAB_DIR/id_ed25519"

# repo root (this script lives in scripts/)
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
die()  { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

command -v orbctl >/dev/null || die "orbctl not found — install OrbStack"

controllers() { local i; for ((i=1;i<=CTLS;i++)); do echo "${PREFIX}-ctl${i}"; done; }
computes()    { local i; for ((i=1;i<=CMPS;i++)); do echo "${PREFIX}-cmp${i}"; done; }
targets()     { controllers; computes; }
all_machines(){ echo "$DEPLOY"; targets; }

machine_exists() { orbctl info "$1" >/dev/null 2>&1; }
machine_ip()     { orbctl info "$1" --format json | python3 -c 'import sys,json;print(json.load(sys.stdin)["ip4"])'; }

create_machine() {
  local name="$1"
  if machine_exists "$name"; then
    ok "reuse $name"
    return
  fi
  log "create $name ($DISTRO${ARCH:+ $ARCH})"
  if [[ -n "$ARCH" ]]; then
    orbctl create -a "$ARCH" "$DISTRO" "$name" >/dev/null
  else
    orbctl create "$DISTRO" "$name" >/dev/null
  fi
  ok "created $name"
}

# provision_target installs sshd + the shared public key + python3 (for ansible).
provision_target() {
  local name="$1" pub="$2"
  log "provision $name (sshd + key)"
  orb -m "$name" -u root bash -s "$pub" <<'EOF'
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq openssh-server python3 >/dev/null 2>&1
mkdir -p /root/.ssh && chmod 700 /root/.ssh
grep -qxF "$1" /root/.ssh/authorized_keys 2>/dev/null || echo "$1" >> /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
systemctl enable ssh >/dev/null 2>&1
systemctl restart ssh
EOF
}

# provision_deployer = target provisioning + git/curl + the private key + an ssh
# config so ansible accepts target host keys on first connect.
provision_deployer() {
  local name="$1" pub="$2"
  provision_target "$name" "$pub"
  log "provision $name (deployer: git/curl + private key)"
  orb -m "$name" -u root bash -c '
    export DEBIAN_FRONTEND=noninteractive
    apt-get install -y -qq git curl >/dev/null 2>&1
    install -m 600 /dev/stdin /root/.ssh/id_ed25519
    cat > /root/.ssh/config <<CFG
Host *
  StrictHostKeyChecking accept-new
  UserKnownHostsFile /root/.ssh/known_hosts
CFG
    chmod 600 /root/.ssh/config
  ' < "$KEY"
}

write_cluster_yaml() {
  local out="$1" mode="$2" deploy_ip="$3"
  {
    echo "name: ${PREFIX}-lab"
    echo "region: RegionOne"
    echo "domain: ${DOMAIN}"
    echo "ansible_port: 22"
    echo "deployment:"
    [[ "$mode" == "local" ]] && echo "  mode: local"
    echo "  host: ${deploy_ip}"
    echo "  user: root"
    echo "  port: 22"
    [[ "$mode" != "local" ]] && echo "  key_path: ${KEY}"
    echo "k8s:"
    echo "  cluster_name: cluster.local"
    echo "  kube_ovn_iface: br-overlay"
    echo "  kube_service_addresses: 10.233.0.0/18"
    echo "  kube_pods_subnet: 10.236.0.0/14"
    echo "nodes:"
    local n
    for n in $(controllers); do
      echo "  - name: ${n}"
      echo "    ansible_host: $(machine_ip "$n")"
      echo "    roles: [controller]"
    done
    for n in $(computes); do
      echo "  - name: ${n}"
      echo "    ansible_host: $(machine_ip "$n")"
      echo "    roles: [compute]"
    done
  } > "$out"
}

cmd_up() {
  mkdir -p "$LAB_DIR"
  [[ -f "$KEY" ]] || { log "generate lab SSH key"; ssh-keygen -t ed25519 -N '' -f "$KEY" -q -C "genestack-lab"; }
  local pub; pub="$(cat "$KEY.pub")"

  local name
  for name in $(all_machines); do create_machine "$name"; done

  provision_deployer "$DEPLOY" "$pub"
  for name in $(targets); do provision_target "$name" "$pub"; done

  local deploy_ip; deploy_ip="$(machine_ip "$DEPLOY")"

  write_cluster_yaml "$LAB_DIR/cluster.yaml" ssh "$deploy_ip"
  ok "wrote $LAB_DIR/cluster.yaml (ssh mode → ${deploy_ip})"

  # Smoke test: deployer can reach every target as root.
  log "smoke test: deployer → targets over SSH"
  for name in $(targets); do
    local tip; tip="$(machine_ip "$name")"
    if orb -m "$DEPLOY" -u root ssh -o BatchMode=yes -o ConnectTimeout=8 "root@${tip}" true 2>/dev/null; then
      ok "  ${name} (${tip}) reachable"
    else
      warn "  ${name} (${tip}) NOT reachable from deployer"
    fi
  done

  # Optional: build a Linux binary + local-mode config inside the deployer.
  if command -v go >/dev/null; then
    local arch; arch="$(orbctl info "$DEPLOY" --format json | python3 -c 'import sys,json;print(json.load(sys.stdin)["record"]["image"]["arch"])')"
    log "build genestack for linux/${arch} and install into ${DEPLOY}"
    ( cd "$ROOT" && GOOS=linux GOARCH="$arch" go build -o "$LAB_DIR/genestack.linux" ./cmd/genestack )
    orb -m "$DEPLOY" -u root bash -c 'install -m 0755 /dev/stdin /usr/local/bin/genestack' < "$LAB_DIR/genestack.linux"
    write_cluster_yaml "$LAB_DIR/cluster.local.yaml" local "$deploy_ip"
    orb -m "$DEPLOY" -u root bash -c 'install -m 0644 /dev/stdin /root/cluster.yaml' < "$LAB_DIR/cluster.local.yaml"
    ok "installed genestack + /root/cluster.yaml in ${DEPLOY} (local mode)"
  else
    warn "go not found — skipping local-mode binary (ssh mode still works)"
  fi

  cat <<NEXT

$(ok "lab ready")

Recommended on macOS — local mode inside the deployer (the genestack binary was
installed there). This is the reliable path: Go's net stack cannot dial
OrbStack's IFSCOPE'd bridge IPs from macOS, so SSH mode from the Mac fails with
"no route to host" even though plain ssh works. Local mode avoids that hop.

  scripts/orbstack-lab.sh run connect
  scripts/orbstack-lab.sh run inventory
  scripts/orbstack-lab.sh run overrides list
  scripts/orbstack-lab.sh run run --all --dry-run
  scripts/orbstack-lab.sh run run config        # clone + bootstrap + upload

(equivalent to: orb -m $DEPLOY -u root genestack --config /root/cluster.yaml --local ...)

SSH mode from the Mac ($LAB_DIR/cluster.yaml) works on a normal routable
deployment host, just not against OrbStack IPs on macOS.

Tear down:
  scripts/orbstack-lab.sh down
NEXT
}

# cmd_run execs the genestack binary in local mode inside the deployer.
cmd_run() {
  machine_exists "$DEPLOY" || die "$DEPLOY does not exist — run 'up' first"
  orb -m "$DEPLOY" -u root genestack --config /root/cluster.yaml --local "$@"
}

cmd_status() {
  orbctl list
  echo
  local name
  for name in $(all_machines); do
    if machine_exists "$name"; then
      printf '%-14s %s\n' "$name" "$(machine_ip "$name")"
    fi
  done
}

cmd_ssh() {
  machine_exists "$DEPLOY" || die "$DEPLOY does not exist — run 'up' first"
  exec ssh -i "$KEY" -o StrictHostKeyChecking=accept-new "root@$(machine_ip "$DEPLOY")"
}

cmd_down() {
  local existing=()
  local name
  for name in $(all_machines); do
    machine_exists "$name" && existing+=("$name")
  done
  if [[ ${#existing[@]} -eq 0 ]]; then
    warn "no lab machines to delete"
    return
  fi
  log "deleting: ${existing[*]}"
  orbctl delete -f "${existing[@]}"
  ok "deleted ${#existing[@]} machine(s)"
}

case "${1:-up}" in
  up)     cmd_up ;;
  status) cmd_status ;;
  ssh)    cmd_ssh ;;
  run)    shift; cmd_run "$@" ;;
  down|destroy) cmd_down ;;
  *) die "unknown command '${1}' (use: up | status | ssh | run | down)" ;;
esac
