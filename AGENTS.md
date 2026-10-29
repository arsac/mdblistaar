# mdblistaar

A run-once Go binary, deployed as a Kubernetes CronJob: each run (a Sync) reads mdblist.com Source Lists and adds missing movies to Radarr and missing Shows to Sonarr.

- Domain terms: [GLOSSARY.md](GLOSSARY.md). Decisions: [docs/adr/](docs/adr/).
- Toolchain versions live in `mise.toml` only; CI installs from it.

## Commands

```sh
mise install                 # Go + golangci-lint at the pinned versions
mise exec -- go test -race ./...
mise exec -- golangci-lint run ./...
mise exec -- govulncheck ./...                 # known vulnerabilities in code we call
mise exec -- zizmor .github/workflows/         # workflow security
mise run test:integration    # real Sonarr + Radarr (compose.yaml) and the live mdblist API
lefthook install             # pre-commit lint
```

Integration tests need `MDBLIST_API_KEY` in `.env` (gitignored, loaded by mise). `docker compose down` resets the Arrs.

## Rules

- A Sync only adds. Never modify or delete anything in an Arr (see ADR 0001).
- Tests are blackbox: `package foo_test`, exported API only, no `export_test.go`. Lint enforces this.
- Fake only at the HTTP edge (`httptest` servers for mdblist, Sonarr, Radarr). No interfaces that exist only for mocking.
- The Arrs are reached through `golift.io/starr` and the config file is parsed with `go.yaml.in/yaml/v3`; everything else is stdlib.
- API keys must never reach logs or errors, including inside logged URLs.
- Commits follow Conventional Commits; release-please derives versions from them.
