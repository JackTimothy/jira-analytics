#!/usr/bin/env bash
#
# Prepares the container so that `go test ./...` and `npm run build` both work
# on first try. Run once at container creation.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> Go modules"
go mod download

echo "==> Go tooling"
# gopls powers the editor; golangci-lint is what the settings point the lint
# action at. Installed here rather than left to the extension's auto-update so
# the container is reproducible and works offline after creation.
go install golang.org/x/tools/gopls@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

echo "==> Web dependencies"
if [ -f web/package-lock.json ]; then
  npm --prefix web ci
else
  npm --prefix web install
fi

echo "==> Project configuration"
if [ ! -f projects.yaml ]; then
  cp projects.example.yaml projects.yaml
  echo "    Created projects.yaml from the example — edit it with your tracker"
  echo "    project and repositories before running the server."
fi

cat <<'NOTE'

Ready.

  go test ./...                 run the Go suite
  (cd web && npm run build)     build the client (typechecks as it goes)
  go run ./cmd/server           serve the API and the built client on :8080
  (cd web && npm run dev)       live client on :5173, proxying the API to :8080

Credentials come from the environment. Export these on your host before opening
the container and they pass through automatically:

  JIRA_BASE_URL  JIRA_EMAIL  JIRA_API_TOKEN  GITHUB_TOKEN

NOTE
