#!/usr/bin/env bash
set -euo pipefail

docker_archive="${1:-}"
[[ -f "${docker_archive}" && ! -L "${docker_archive}" ]] || {
  echo "usage: $0 /absolute/path/docker-VERSION.tgz" >&2
  exit 2
}
[[ "${EUID}" -eq 0 ]] || { echo "run as root" >&2; exit 2; }

work_directory="$(mktemp -d /var/tmp/clusterguard-docker.XXXXXX)"
trap 'rm -rf "${work_directory}"' EXIT

tar -tzf "${docker_archive}" | awk '
  BEGIN { ok=1 }
  /^docker\/$/ { next }
  /^docker\/(containerd|containerd-shim-runc-v2|ctr|docker|docker-init|docker-proxy|dockerd|runc)$/ { next }
  { ok=0 }
  END { exit ok ? 0 : 1 }
' || { echo "Docker static archive contains an unexpected path" >&2; exit 3; }
tar -xzf "${docker_archive}" -C "${work_directory}"
for binary in containerd containerd-shim-runc-v2 ctr docker docker-init docker-proxy dockerd runc; do
  install -m 0755 "${work_directory}/docker/${binary}" "/usr/local/bin/${binary}"
done

install -d -m 0755 /etc/docker /etc/systemd/system
if [[ ! -e /etc/docker/daemon.json ]]; then
  install -m 0644 /dev/null /etc/docker/daemon.json
  selinux_enabled=false
  if command -v getenforce >/dev/null 2>&1 && [[ "$(getenforce)" != "Disabled" ]]; then
    selinux_enabled=true
  fi
  printf '%s\n' "{\"data-root\":\"/var/lib/docker\",\"log-driver\":\"local\",\"log-opts\":{\"max-size\":\"100m\",\"max-file\":\"5\"},\"storage-driver\":\"overlay2\",\"selinux-enabled\":${selinux_enabled}}" >/etc/docker/daemon.json
fi
if grep -Eq '"live-restore"[[:space:]]*:[[:space:]]*true' /etc/docker/daemon.json; then
  echo "Docker live-restore=true is incompatible with Swarm mode; disable it before installing the Swarm runtime" >&2
  exit 3
fi

install -m 0644 /dev/null /etc/systemd/system/docker.service
cat >/etc/systemd/system/docker.service <<'UNIT'
[Unit]
Description=Docker Application Container Engine
Documentation=https://docs.docker.com/
After=network-online.target firewalld.service
Wants=network-online.target

[Service]
Type=notify
# The static distribution has no separately managed containerd.service.
# Dockerd starts and supervises its bundled containerd when --containerd is omitted.
ExecStart=/usr/local/bin/dockerd --host=unix:///run/docker.sock
ExecReload=/bin/kill -s HUP $MAINPID
TimeoutStartSec=0
Restart=always
RestartSec=2
StartLimitBurst=3
StartLimitIntervalSec=60
LimitNOFILE=infinity
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
Delegate=yes
KillMode=process
OOMScoreAdjust=-500

[Install]
WantedBy=multi-user.target
UNIT

cat >/etc/modules-load.d/clusterguard-docker.conf <<'EOF'
overlay
br_netfilter
EOF
modprobe overlay
modprobe br_netfilter
cat >/etc/sysctl.d/99-clusterguard-docker.conf <<'EOF'
net.ipv4.ip_forward = 1
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-ip6tables = 1
EOF
sysctl --system >/dev/null

if systemctl is-active --quiet firewalld; then
  firewall-cmd --permanent --add-port=2377/tcp >/dev/null
  firewall-cmd --permanent --add-port=7946/tcp >/dev/null
  firewall-cmd --permanent --add-port=7946/udp >/dev/null
  firewall-cmd --permanent --add-port=4789/udp >/dev/null
  firewall-cmd --permanent --add-port=3306/tcp >/dev/null
  firewall-cmd --reload >/dev/null
fi

systemctl daemon-reload
systemctl enable --now docker.service
/usr/local/bin/docker version --format 'Docker {{.Server.Version}} is ready'
