# houston-runner-N: the Docker CLI (with compose and buildx), git and ssh.
# The houston CLI itself is the host's, mounted read-only by the installer.
FROM docker:29-cli
RUN apk add --no-cache git openssh-client
