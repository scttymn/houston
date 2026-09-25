# Security

## Reporting a vulnerability

Please report it privately: **Security › Report a vulnerability** on [the repository](https://github.com/scttymn/houston/security/advisories/new). Don't open a public issue.

Include what you found, how to reproduce it, and what it lets someone do. Houston is maintained by one person, so replies are best effort, usually within a week. A fix ships as a new release, and the advisory is published after it, crediting you unless you'd rather it didn't.

## Supported versions

Only the latest release gets fixes. The flight board and `houston status` say when a newer one is out. To update, run the installer again.

## What Houston trusts

This is how Houston is designed. If Houston is less safe than this, that's a vulnerability:

- **Anyone who can push to a linked repo's deploy branch runs code on the server**, in Docker: its image, its tests and its release hook. That code reaches only its own project: its build reads only its own checkout, it deploys only as the project Houston claimed it for, and it never sees the runner's token.
- **Mission Control's admin** can do everything: deploys, secrets, restores and Cloudflare. So can an API token, which acts as the admin.
- **The internet** reaches Mission Control only through the Cloudflare Tunnel, at `admin.<base>` (sign-in required) and `hooks.<base>` (only webhooks, each checked against its project's secret).
- **Port 3000** is plain HTTP, open to the network only until setup is done. After that, rerunning the installer binds it to `127.0.0.1`.
- **The server's root and the `houston` user** are trusted. The `houston` user is in the `docker` group, which amounts to root.
- **Secrets** are encrypted at rest with keys in `/opt/houston/.env`. They're written only to their destination, and never to a log or a command line.
- **The local registry** (`127.0.0.1:5000`) has no authentication. Only processes on the server can reach it.
