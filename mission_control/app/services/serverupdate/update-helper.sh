# houston-update: updates this server to a release, started from Mission
# Control (docs/plans/update-from-mission-control.md). It runs in a
# privileged container in the host's PID namespace, and enters the host to
# run the release's own install.sh there as root, as `sudo sh` would: a
# clean environment, the host's PATH, curl and systemd. Each install gets
# 20 minutes. If the new version doesn't install, the version the server ran
# is installed again.
#
# In: HOUSTON_UPDATE_TO, HOUSTON_UPDATE_FROM (release tags), HOUSTON_REPO,
# and the installer's HOUSTON_DIR and HOUSTON_RUNNERS.
# Exit: 0 installed; 3 failed, and the old version is back; 1 both failed.
set -u

install() {
  echo "==> houston update: installing $1"
  nsenter -t 1 -m -u -i -n -- env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=/root \
    VERSION="$1" REPO="$HOUSTON_REPO" DIR="$HOUSTON_DIR" RUNNERS="$HOUSTON_RUNNERS" \
    timeout 20m sh -c '
      f=$(mktemp) || exit 1
      curl -fsSL -o "$f" "https://github.com/$REPO/releases/download/$VERSION/install.sh" &&
        HOUSTON_VERSION="$VERSION" HOUSTON_REPO="$REPO" HOUSTON_DIR="$DIR" HOUSTON_RUNNERS="$RUNNERS" sh "$f"
      status=$?
      rm -f "$f"
      exit "$status"'
}

install "$HOUSTON_UPDATE_TO" && exit 0
echo "==> houston update: $HOUSTON_UPDATE_TO didn't install; putting $HOUSTON_UPDATE_FROM back"
install "$HOUSTON_UPDATE_FROM" && exit 3
echo "==> houston update: $HOUSTON_UPDATE_FROM didn't install either; run the installer on the server"
exit 1
