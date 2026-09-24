# Plan: the installer, by package manager

## Your direction
- "I think we should focus rather on supported package managers and less on X distro. apt, pacman, and whatever fedora/redhat uses are probably a safe baseline."
- "lets just detect the installers we support and warn/halt if missing"

This replaces the spec's "Supports Ubuntu LTS and Debian stable" (§11), and the README's line with it.

## Goal
`install/install.sh` installs Houston on any Linux whose package manager it knows: **apt** (Debian, Ubuntu and their derivatives), **dnf** (Fedora, RHEL, Rocky, Alma, CentOS Stream) and **pacman** (Arch and its derivatives). It picks the package manager present, not a distro name. On a system with none of the three it stops, and says which it supports.

## What's distro-specific today (evidence)
- `check_system` refuses anything whose `/etc/os-release` `ID` isn't `ubuntu` or `debian`.
- `install_docker` uses apt only: Docker's apt repository (`download.docker.com/linux/$ID`), then `apt-get install` for git and openssh-server.
- Everything else is generic Linux:
  - `useradd`, `usermod`, `ssh-keygen`
  - `systemctl enable --now docker` (its failure is tolerated)
  - the files in `/opt/houston`, and Docker
- **Unstated today:** Kamal SSHes to this host as `houston`, so an SSH server must be *running*. Debian and Ubuntu start `ssh` on install; Fedora and Arch don't.

## Design (short)
- **Prerequisites** (one list, checked the same way everywhere): Docker with the Compose plugin, git, an OpenSSH server that's running, `useradd`, curl, and bash (the `houston` user's shell, found on `PATH`, not assumed at `/bin/bash`).
- **Choose by what's installed:** `apt-get`, then `dnf`, then `pacman`. The first one found installs whatever's missing:

| | Docker | git, SSH server | services |
|---|---|---|---|
| **apt** | Docker's apt repository for `debian` or `ubuntu`, taken from `ID`, or `ID_LIKE` for derivatives such as Mint and Pop!_OS, with `VERSION_CODENAME` or `UBUNTU_CODENAME` | `git`, `openssh-server` | `docker`, `ssh` |
| **dnf** | Docker's repository for `fedora`, or for `rhel`/`centos` on the RHEL family (`dnf config-manager`, both the dnf4 and dnf5 syntax) | `git`, `openssh-server` | `docker`, `sshd` |
| **pacman** | `docker`, `docker-compose` (Arch ships it as the CLI plugin) and `docker-buildx` from the distro | `git`, `openssh` | `docker`, `sshd` |

  Services are enabled and started with `systemctl enable --now`.
- **None of the three:** it stops before changing anything: "Houston installs with apt, dnf or pacman; this system has none of them." Nothing is refused by distro name.
- **An SSH server that's installed but stopped** is started (`ssh` or `sshd`). Then the installer checks that `houston` can SSH to this host before it finishes, and says so if it can't. That check holds on every package manager, not only the three.
- **SELinux** (enforcing by default on Fedora and the RHEL family): same rule. If `getenforce` says `Enforcing`, the installer stops before changing anything, saying to set it to permissive (`setenforce 0`, and `SELINUX=permissive` in `/etc/selinux/config`), since Houston doesn't label its containers for SELinux yet.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | apt: Ubuntu and Debian install and finish setup as today (the existing full run) | `orbstack.sh ubuntu:noble`, `orbstack.sh debian:bookworm` | Parity |
| 2 | dnf: Fedora installs Docker from Docker's repository, starts `sshd`, and the full run passes | `orbstack.sh fedora` | Contract |
| 3 | dnf on the RHEL family: Rocky 9 (Docker's `rhel`/`centos` repository) | `orbstack.sh rocky:9` | Contract |
| 4 | pacman: Arch installs `docker`, `docker-compose`, `docker-buildx`, `git`, `openssh`, starts `sshd`, and the full run passes | `orbstack.sh arch` | Contract |
| 5 | None of apt, dnf and pacman (Alpine), or SELinux enforcing (a Fedora machine set to enforcing) → the message, exit 1, nothing installed and nothing written to `/opt/houston` | `install/test/install-refusals.sh` | Preconditions, Signals |
| 6 | An SSH server installed but stopped is started, and `houston` → `localhost` SSH is checked before the installer reports success | covered by rows 2 and 4 (neither distro starts `sshd` on its own) | Crash & repair |
| 7 | Rerunning on each package manager repairs and updates, with the secrets kept (the existing rerun stage) | `orbstack.sh` on each of rows 1–4 | Re-entry |
| 8 | `shellcheck install/install.sh` is clean, and the README and spec (§11) say "apt, dnf or pacman" | shellcheck, the review | Honest surface |

The full run (`orbstack.sh`) covers the whole install and first run, the tunnel, deploys, pushes and maintenance. Rows 1–4 run it once per package manager, one after another.

## Later (named, not built)
- **NixOS.** Skipped for now ("let's just skip nix for now. I don't have experience yet"). Two ways, when it's time:
  - the installer recognizes it and prints the `configuration.nix` lines Docker and SSH need (`virtualisation.docker.enable`, `services.openssh.enable`)
  - a Houston NixOS module (`services.houston.enable`)

  OrbStack runs NixOS 25.11, so either can get a real run. Until then NixOS falls under "any other Linux": it proceeds if the prerequisites are there. One snag: its bash isn't at `/bin/bash`, so the `houston` user's shell is taken from `PATH`, as on every system.

## Decisions (yours)
1. **Only apt, dnf and pacman.** Anything else stops with a clear message; there's no "prerequisites already installed" fallback.
2. **SELinux enforcing stops the install** (following "warn/halt"), with the fix named. SELinux support is a later item.
3. **NixOS later** (see above).

## Done
- **The full run (`orbstack.sh`) on the final installer:**
  - Fedora (dnf5, Docker's fedora repository): PASS, 81 of 81
  - Arch (pacman, Arch's docker packages): PASS, 81 of 81
  - Ubuntu 24.04 and Debian 12 (apt): PASS, 81 of 81 each
  - Rocky 9 (dnf4, Docker's centos repository): 80 of 81 at first; then PASS, 82 of 82, on the rerun with the fixed check

  Each run covers the install, first run, the real Cloudflare tunnel, deploys, pushes, the agent path, maintenance, and a rerun of the installer (repair and update, secrets kept). The test tunnel and its DNS records were removed after each, and `equip.svnmns.com` still answered 200.
- **`install-refusals.sh`: REFUSALS PASS.** Each machine is stopped with its message, with no Docker, no Docker repository, no `houston` user and no `/opt/houston` left behind:
  - Alpine (no apt, dnf or pacman)
  - Fedora with SELinux enforcing (simulated with a `getenforce` stub, since OrbStack's kernel doesn't run SELinux)
  - Kali and Devuan (apt, releases Docker doesn't publish for)
- **Rocky's first failure wasn't the installer.** A check after a failed-tests push demanded ten 200s through Cloudflare, but some Cloudflare edges still answered with the base domain's wildcard (the other server's 404) minutes after the name had settled once. Meanwhile kamal-proxy on the server answered 200. The check (`agent-through-tunnel.sh`) now proves the server directly, then settles through Cloudflare like every tunnel stage; the rerun passed with it.
- **What the run found before the review:** the installer's final "Finish setup at" address and the test's machine address both used `hostname -I`, which Arch doesn't ship. Both fall back to `ip route` now.

## Review (scotty-review, cold pass): request changes, all fixed
- **MEDIUM:** dnf's `openssh-server` doesn't bring the `ssh` client, so the new SSH check would fail on a minimal Fedora or RHEL-family server, with a hint pointing the wrong way. The client is now installed too (`openssh-client` on apt, `openssh-clients` on dnf; `openssh` has both on pacman).
- **MEDIUM:** a derivative Docker doesn't publish for (Kali, Devuan, LMDE on apt; Amazon Linux on dnf) got a repository written anyway. Its failure then broke every later `apt-get update`, and dnf4's `-q` hid the error completely. Now:
  - apt uses `DEBIAN_CODENAME` (LMDE), and checks the repository's `Release` exists before writing anything
  - dnf picks `fedora` only for Fedora itself, `rhel` for RHEL, and CentOS's repository for anything whose `ID_LIKE` names rhel or centos; everything else is refused by name
  - a failed install removes the repository file
  - `pm_install` stops with the exact command to run by hand
- **MEDIUM:** `pacman -Syu` can replace the running kernel, and Docker's daemon then can't start until a reboot, which the old check (the client only) couldn't see. `docker info` is now checked. If the running kernel's modules are gone, the message says to reboot and rerun.
- **LOW:** the SSH check no longer pins this host's key (Kamal doesn't read houston's `known_hosts` either), so changed host keys don't block a rerun.
- **LOW:** the architecture and `useradd` checks moved to the start, so "stops, changing nothing" holds for every up-front refusal.
- **My own, found while fixing:** the first dnf repository match checked for `fedora` first, and Rocky's `ID_LIKE` ("rhel centos fedora") would have got Fedora's repository.

## Later (also)
- **Oracle Linux and Amazon Linux:** they install with dnf, but their `ID_LIKE` names only fedora, so they're refused with "install Docker yourself". Oracle Linux could use CentOS's repository if someone needs it.
- **SELinux support:** labels on Houston's own containers and Kamal's, and a real run on a machine that enforces it.
