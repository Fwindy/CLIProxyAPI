#!/usr/bin/env bash

if [[ -z "${BASH_VERSION:-}" ]]; then
  exec bash "$0" "$@"
fi

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"

REMOTE_HOST="${REMOTE_HOST:-us}"
REMOTE_PATH="${REMOTE_PATH:-/root/cliproxyapi/cli-proxy-api}"
SYSTEMD_SERVICE="${SYSTEMD_SERVICE:-cliproxyapi.service}"
MODELS_REPO_URL="${MODELS_REPO_URL:-https://github.com/router-for-me/models.git}"
OUTPUT_BIN="${OUTPUT_BIN:-${REPO_ROOT}/dist/cli-proxy-api_linux_amd64}"
SSH_CONFIG_FILE="${SSH_CONFIG_FILE:-${HOME}/.ssh/config}"
BUILD_DIR=""
SSH_CMD=(ssh)
SCP_CMD=(scp)

if [[ -f "${SSH_CONFIG_FILE}" ]]; then
  SSH_CMD+=(-F "${SSH_CONFIG_FILE}")
  SCP_CMD+=(-F "${SSH_CONFIG_FILE}")
fi

log() {
  printf '[deploy-us] %s\n' "$*"
}

cleanup() {
  if [[ -n "${BUILD_DIR}" && -d "${BUILD_DIR}" ]]; then
    rm -rf "${BUILD_DIR}"
  fi
}

trap cleanup EXIT

require_clean_worktree() {
  if ! git diff --quiet --ignore-submodules -- || ! git diff --cached --quiet --ignore-submodules --; then
    log "working tree is dirty; commit or stash changes before syncing"
    exit 1
  fi
}

pick_sync_remote() {
  if git remote get-url upstream >/dev/null 2>&1; then
    printf 'upstream\n'
    return
  fi
  if git remote get-url origin >/dev/null 2>&1; then
    printf 'origin\n'
    return
  fi
  log "no git remote named upstream or origin found"
  exit 1
}

detect_sync_branch() {
  local remote="$1"
  local current_branch
  current_branch="$(git branch --show-current)"

  if [[ -n "${SYNC_BRANCH:-}" ]]; then
    printf '%s\n' "${SYNC_BRANCH}"
    return
  fi

  if [[ -n "${current_branch}" ]]; then
    printf '%s\n' "${current_branch}"
    return
  fi

  printf 'main\n'
}

sync_source() {
  local remote="$1"
  local branch="$2"
  local remote_ref ahead behind

  log "fetching ${remote}/${branch}"
  git fetch --force --tags "${remote}" "${branch}"
  remote_ref="${remote}/${branch}"
  read -r ahead behind < <(git rev-list --left-right --count HEAD..."${remote_ref}")

  if [[ "${ahead}" == "0" && "${behind}" == "0" ]]; then
    log "current branch already matches ${remote_ref}"
    return
  fi

  if [[ "${ahead}" == "0" ]]; then
    log "fast-forwarding current branch with ${remote_ref}"
    git merge --ff-only "${remote_ref}"
    return
  fi

  if [[ "${behind}" == "0" ]]; then
    log "current branch already contains ${remote_ref}; keeping local commits on top"
    return
  fi

  log "rebasing current branch onto ${remote_ref}"
  git rebase "${remote_ref}"
}

prepare_build_tree() {
  BUILD_DIR="$(mktemp -d)"

  log "exporting source tree to ${BUILD_DIR}"
  git archive HEAD | tar -x -C "${BUILD_DIR}"

  log "refreshing models catalog from ${MODELS_REPO_URL}"
  git fetch --depth 1 "${MODELS_REPO_URL}" main
  git show FETCH_HEAD:models.json > "${BUILD_DIR}/internal/registry/models/models.json"
}

build_binary() {
  local version commit build_date ldflags

  version="${VERSION:-$(git describe --tags --always)}"
  commit="${COMMIT:-$(git rev-parse --short HEAD)}"
  build_date="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
  ldflags="-s -w -X main.Version=${version} -X main.Commit=${commit} -X main.BuildDate=${build_date}"

  mkdir -p "$(dirname "${OUTPUT_BIN}")"

  log "building linux/amd64 binary"
  (
    cd "${BUILD_DIR}"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go build -buildvcs=false -trimpath -ldflags "${ldflags}" -o "${OUTPUT_BIN}" ./cmd/server/
  )
}

deploy_remote() {
  local remote_tmp
  remote_tmp="${REMOTE_PATH}.tmp.$$"

  log "uploading binary to ${REMOTE_HOST}:${REMOTE_PATH}"
  "${SSH_CMD[@]}" "${REMOTE_HOST}" "mkdir -p '$(dirname "${REMOTE_PATH}")'"
  "${SCP_CMD[@]}" "${OUTPUT_BIN}" "${REMOTE_HOST}:${remote_tmp}"
  "${SSH_CMD[@]}" "${REMOTE_HOST}" "\
    set -euo pipefail; \
    install -m 755 '${remote_tmp}' '${REMOTE_PATH}'; \
    rm -f '${remote_tmp}'; \
    systemctl --user restart '${SYSTEMD_SERVICE}'; \
    systemctl --user is-active --quiet '${SYSTEMD_SERVICE}'"
}

main() {
  local remote branch

  require_clean_worktree
  remote="$(pick_sync_remote)"
  branch="$(detect_sync_branch "${remote}")"

  sync_source "${remote}" "${branch}"
  prepare_build_tree
  build_binary
  deploy_remote

  log "deploy finished: ${OUTPUT_BIN} -> ${REMOTE_HOST}:${REMOTE_PATH}"
}

main "$@"
