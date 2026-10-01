# Contributing to AgentHub

Thanks for looking. Bug reports, questions and pull requests are all welcome,
in English or 中文 — issues and pull requests can be written in either.

For a security problem, do not open an issue: see [SECURITY.md](SECURITY.md).

## What you need

- **Go 1.27.0 or newer** for both modules (`go.mod` and `desktop/go.mod`). The
  floor is a security requirement, not a language one;
  [Build and test](docs/developer.md#build-and-test) in the developer guide
  says why.
- **Node 22** for the desktop frontend. It is not optional: the desktop Go
  tests run the frontend checks through `node`.
- On Linux, the desktop module needs GTK 3 and WebKit2GTK 4.1 headers to
  compile (`libgtk-3-dev libwebkit2gtk-4.1-dev` on Debian/Ubuntu).
- `shellcheck`, if you touch `install.sh` or `scripts/`.

## Build and test

These are the commands [CI](.github/workflows/ci.yml) runs. A pull request
needs all of them green.

**Root module** (`agenthub-node`, `ah`, `agenthub-mcp`), from the repository
root:

```sh
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go build ./...
```

**Desktop frontend**, in `desktop/frontend`:

```sh
npm ci
npm run build
npm test
```

**Desktop module**, in `desktop`. `main.go` embeds `frontend/dist`, so the
module does not compile until the frontend has been built — run
`npm ci && npm run build` above first:

```sh
go vet ./...
go test -race -count=1 ./...
```

**Install script and hygiene**, from the repository root:

```sh
sh -n install.sh
shellcheck -s sh install.sh
shellcheck scripts/test-install.sh
bash scripts/test-install.sh
gofmt -l .                                   # must print nothing
git diff --check origin/main...HEAD -- . ':(exclude)desktop/frontend/wailsjs'
./.github/scripts/secret-scan.sh --self-test
./.github/scripts/secret-scan.sh
```

CI also runs `govulncheck` and `gosec` on both modules, `npm audit`, a build
for six platforms, and the process-reading tests on real Windows and macOS
runners. Those need tools or machines you may not have; CI will tell you.

`desktop/frontend/wailsjs` is generated. Do not edit it by hand: CI regenerates
it and fails the pull request if the two differ.

To look at the window without a node, run `npx vite` in `desktop/frontend` and
open `/dev/mock.html` — fake data, every state reachable from the URL (see the
comments in `dev/mock.js`).

## The desktop app

- [`desktop/docs/ui-contract.md`](desktop/docs/ui-contract.md) is the desktop
  app's functional contract: every control, binding and tested behaviour the
  window has to keep. A change that removes or changes one updates the contract
  in the same pull request.
- Every UI string exists in both languages: `desktop/frontend/src/i18n/en.js`
  and `desktop/frontend/src/i18n/zh-Hant.js`, under the same key. The markup
  carries keys, not sentences.

## Commits and pull requests

- Commit subjects use a conventional prefix, with a scope where it helps:
  `feat(desktop): ...`, `fix(install): ...`, `docs: ...`, `test(desktop): ...`,
  `chore: ...`. One line of subject, then a short body saying what changed and
  why.
- Keep a pull request to one change. Say what you did, how you checked it, and
  whether it touches the UI contract or the translations — the template asks.
- A pull request is not merged until every CI check is green.

## Reporting a bug

Use the bug report form. Include `ah --version`, your OS and how you installed.
Before pasting a log, remove session contents, node ids, host names and
addresses.
