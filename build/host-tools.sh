#!/bin/bash
# Installs the host tools build/host.env pins, as root, on Ubuntu: the release builder's (infra
# builder/user-data.sh runs it from the commit it builds), so a release is made with the versions
# the repository was tested with and never Ubuntu's older ones. It runs before task exists, which
# is why it is a script and not a task.
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=build/host.env
. build/host.env

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# fetch downloads url to file and checks its SHA-256.
fetch() {
  curl -sfL --retry 3 -o "$2" "$1"
  echo "$3  $2" | sha256sum -c --quiet -
}

fetch "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" "$tmp/go.tar.gz" "$GO_SHA256"
rm -rf /usr/local/go
tar -C /usr/local -xzf "$tmp/go.tar.gz"
ln -sf /usr/local/go/bin/go /usr/local/bin/go
ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

fetch "https://github.com/go-task/task/releases/download/v${TASK_VERSION}/task_linux_amd64.tar.gz" "$tmp/task.tar.gz" "$TASK_SHA256"
tar -C /usr/local/bin -xzf "$tmp/task.tar.gz" task

fetch "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.tar.gz" "$tmp/gh.tar.gz" "$GH_SHA256"
tar -C "$tmp" -xzf "$tmp/gh.tar.gz"
install -m 0755 "$tmp/gh_${GH_VERSION}_linux_amd64/bin/gh" /usr/local/bin/gh

# Docker's repository, its key checked by fingerprint before apt trusts it.
curl -sfL --retry 3 -o "$tmp/docker.asc" https://download.docker.com/linux/ubuntu/gpg
fpr=$(gpg --show-keys --with-colons "$tmp/docker.asc" | awk -F: '/^fpr/{print $10; exit}')
[ "$fpr" = "$DOCKER_APT_KEY" ] || { echo "host-tools: Docker's apt key is $fpr, not $DOCKER_APT_KEY" >&2; exit 1; }
install -D -m 0644 "$tmp/docker.asc" /etc/apt/keyrings/docker.asc
. /etc/os-release
echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $VERSION_CODENAME stable" > /etc/apt/sources.list.d/docker.list
apt-get update -q
# shellcheck disable=SC2086 # a list of packages
apt-get install -qy $HOST_PACKAGES
suffix="-1~ubuntu.${VERSION_ID}~${VERSION_CODENAME}"
apt-get install -qy --allow-downgrades \
  "docker-ce=5:${DOCKER_VERSION}${suffix}" "docker-ce-cli=5:${DOCKER_VERSION}${suffix}" \
  "docker-buildx-plugin=${DOCKER_BUILDX_VERSION}${suffix}" containerd.io

# What the harness opens by path or runs, there before the hour of building that precedes it.
for f in /usr/share/OVMF/OVMF_CODE_4M.secboot.fd /usr/share/OVMF/OVMF_VARS_4M.fd; do
  test -f "$f" || { echo "host-tools: no $f" >&2; exit 1; }
done
for c in mcopy mdel mkfs.vfat ssh docker go task gh; do
  command -v "$c" >/dev/null || { echo "host-tools: no $c" >&2; exit 1; }
done

go version
task --version
gh --version | head -1
docker --version
