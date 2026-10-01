# A throwaway Linux server for the installer's tests: the distro's image
# (BASE) with systemd as its init, as on a real server.
ARG BASE=ubuntu:24.04
FROM ${BASE}
RUN if command -v apt-get >/dev/null; then \
      apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
        systemd systemd-sysv dbus curl ca-certificates iproute2 procps sudo >/dev/null && rm -rf /var/lib/apt/lists/*; \
    elif command -v dnf >/dev/null; then \
      dnf install -y -q systemd curl iproute procps-ng sudo && dnf clean all; \
    elif command -v pacman >/dev/null; then \
      pacman -Sy --noconfirm --needed systemd curl iproute2 procps-ng sudo >/dev/null; \
    fi
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
