# machine.sh: throwaway Linux servers for the installer's tests, with
# Docker alone. Source it:
#
#   . install/test/machine.sh
#   machine_create ubuntu:24.04 houston-test    # any distro's image
#   machine houston-test docker compose version # a command in it, as root
#   machine_ip houston-test
#   machine_delete houston-test
#
# A machine is a privileged container running systemd, so Docker inside it
# runs as on a server (its storage on volumes of its own, not on the
# container's overlay). The checkout is mounted at the same path, as tests
# pass "$repo" paths in. The host may not reach a machine's address (with
# rootless Docker it can't), so tests reach it from inside: machine NAME
# curl http://$(machine_ip NAME):3000/.

_machine_repo="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../.." && pwd)"

machine_create() {
  local image="$1" name="$2" tag
  tag="houston-test-machine:$(printf '%s' "$image" | tr ':/' '--')"
  docker build -q --build-arg BASE="$image" -t "$tag" -f "$_machine_repo/install/test/machine.Dockerfile" "$_machine_repo/install/test" >/dev/null || return 1
  machine_delete "$name"
  docker run -d --name "$name" --hostname "$name" --privileged --cgroupns=private --tmpfs /run --tmpfs /run/lock \
    -v "$name-docker:/var/lib/docker" -v "$name-containerd:/var/lib/containerd" -v "$_machine_repo:$_machine_repo" "$tag" >/dev/null || return 1
  local state=""
  for _ in $(seq 60); do
    state=$(docker exec "$name" systemctl is-system-running 2>/dev/null) || true
    case "$state" in running | degraded) return 0 ;; esac
    sleep 1
  done
  echo "machine $name: systemd didn't come up ($state)" >&2
  return 1
}

# machine NAME [-u USER] COMMAND...: COMMAND in NAME, as root unless -u.
machine() {
  local name="$1" user=root
  shift
  if [ "${1:-}" = -u ]; then user="$2"; shift 2; fi
  docker exec -i -u "$user" "$name" "$@"
}

machine_ip() { docker exec "$1" sh -c "ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if (\$i == \"src\") { print \$(i + 1); exit }}'"; }

machine_delete() {
  docker rm -f "$1" >/dev/null 2>&1 || true
  docker volume rm "$1-docker" "$1-containerd" >/dev/null 2>&1 || true
}
