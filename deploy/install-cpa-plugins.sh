#!/usr/bin/env bash
# Install or update orangeguard on a Docker-based CLIProxyAPI deployment.
# Run on the CPA server as a user that can use docker:
#
#   sudo bash install-cpa-plugins.sh            # install / update orangeguard
#   sudo bash install-cpa-plugins.sh --dry-run  # checks and build only
#   sudo bash install-cpa-plugins.sh --rollback <backup dir>
#
#   --with-key-billing  also replace an installed cpa-key-billing with the
#                       woyin/cpa-plugin-key-billing fork (off by default; an
#                       existing key-billing is otherwise left untouched)
#
# What it does:
#   1. Finds the CPA container whose config.yaml comes from $CPA_DIR and the
#      host directory mounted at /CLIProxyAPI/plugins.
#   2. Builds the plugin(s) inside golang:<ver>-bookworm, matching the glibc of
#      the official Debian bookworm CPA image and the host CPU architecture.
#   3. Stops the container, backs up config.yaml and every library it replaces,
#      installs the library and starts the container again. Existing libraries
#      are replaced in place under their current names (cpa prefers versioned
#      files such as name-v1.2.3.so and the plugin store may pin versions).
#   4. Verifies from the CPA log that the plugins registered, and rolls back
#      automatically if they did not.
#
# config.yaml gets "orangeguard: {enabled: true}" under plugins.configs when it
# is missing (CPA v8 does not load plugins without an explicit entry). Nothing
# else changes: orangeguard does nothing until guard/virtual_models rules are
# added.
set -euo pipefail

CPA_DIR="${CPA_DIR:-/opt/cpa}"
GO_IMAGE="${GO_IMAGE:-golang:1.26-bookworm}"
ORANGEGUARD_REPO="${ORANGEGUARD_REPO:-https://github.com/woyin/OrangeGuard.git}"
ORANGEGUARD_REF="${ORANGEGUARD_REF:-main}"
KEYBILLING_REPO="${KEYBILLING_REPO:-https://github.com/woyin/cpa-plugin-key-billing.git}"
KEYBILLING_REF="${KEYBILLING_REF:-main}"
VERIFY_TIMEOUT="${VERIFY_TIMEOUT:-90}"

mode="install"
rollback_dir=""
with_kb=""
usage() { echo "usage: $0 [--dry-run] [--with-key-billing] | --rollback <backup dir>" >&2; exit 2; }
while (( $# > 0 )); do
  case "$1" in
    --dry-run) mode="dry-run" ;;
    --with-key-billing) with_kb=1 ;;
    --rollback) mode="rollback"; rollback_dir="${2:-}"; [[ -n "$rollback_dir" ]] || usage; shift ;;
    *) usage ;;
  esac
  shift
done

log() { printf '==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# plugin_config_state <config> <id>: prints "enabled", "disabled", "missing",
# or "unsupported" when plugins.configs is not a plain block mapping.
plugin_config_state() {
  awk -v id="$2" '
    function indent(l) { match(l, /^ */); return RLENGTH }
    /^[[:space:]]*(#|$)/ { next }
    /^plugins:[[:space:]]*(#.*)?$/ { inplugins = 1; next }
    inplugins && indent($0) == 0 { inplugins = 0 }
    inplugins && $0 ~ /^[[:space:]]+configs:/ {
      if ($0 !~ /^[[:space:]]+configs:[[:space:]]*(#.*)?$/) { print "unsupported"; done = 1; exit }
      inconfigs = 1; cind = indent($0); next
    }
    inconfigs && indent($0) <= cind { inconfigs = 0 }
    inconfigs && !iind { iind = indent($0) }
    inconfigs && indent($0) == iind { initem = ($0 ~ "^[[:space:]]+\"?" id "\"?:"); if (initem) found = 1; next }
    inconfigs && initem && $0 ~ /^[[:space:]]+enabled:[[:space:]]*true/ { enabled = 1 }
    END {
      if (done) exit
      if (!found) print "missing"; else if (enabled) print "enabled"; else print "disabled"
    }' "$1"
}

# add_plugin_config <config> <id>: inserts "<id>:\n  enabled: true" as the
# first entry of plugins.configs, matching the existing indentation. The file
# is rewritten in place because it is a single-file bind mount.
add_plugin_config() {
  local tmp
  tmp="$(mktemp)"
  awk -v id="$2" '
    function indent(l) { match(l, /^ */); return RLENGTH }
    { lines[NR] = $0 }
    END {
      for (i = 1; i <= NR; i++) {
        print lines[i]
        if (!inserted && lines[i] ~ /^plugins:[[:space:]]*(#.*)?$/) inplugins = 1
        if (inplugins && !inserted && lines[i] ~ /^[[:space:]]+configs:[[:space:]]*(#.*)?$/) {
          cind = indent(lines[i]); step = 2
          for (j = i + 1; j <= NR; j++) {
            if (lines[j] ~ /^[[:space:]]*(#|$)/) continue
            if (indent(lines[j]) > cind) step = indent(lines[j]) - cind
            break
          }
          pad = sprintf("%" (cind + step) "s", ""); sub_pad = sprintf("%" (cind + 2 * step) "s", "")
          print pad id ":"
          print sub_pad "enabled: true"
          inserted = 1
        }
      }
      if (!inserted) exit 3
    }' "$1" >"$tmp" || { rm -f "$tmp"; return 1; }
  cat "$tmp" >"$1"
  rm -f "$tmp"
}

command -v docker >/dev/null || die "docker not found"
docker info >/dev/null 2>&1 || die "cannot talk to the docker daemon (run as root or a docker group member)"

# --- 1. locate the CPA container and its mounts ------------------------------
CPA_DIR="$(cd "$CPA_DIR" && pwd -P)" || die "$CPA_DIR does not exist"
container="${CPA_CONTAINER:-}"
if [[ -z "$container" ]]; then
  for name in $(docker ps -a --format '{{.Names}}'); do
    src="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/CLIProxyAPI/config.yaml"}}{{.Source}}{{end}}{{end}}' "$name")"
    if [[ -n "$src" && "$src" == "$CPA_DIR"/* ]]; then
      [[ -z "$container" ]] || die "several containers use $CPA_DIR ($container, $name); set CPA_CONTAINER"
      container="$name"
    fi
  done
fi
[[ -n "$container" ]] || die "no container mounts a config.yaml from $CPA_DIR; set CPA_CONTAINER"

mount_source() {
  docker inspect -f "{{range .Mounts}}{{if eq .Destination \"$1\"}}{{.Source}}{{end}}{{end}}" "$container"
}
config_file="$(mount_source /CLIProxyAPI/config.yaml)"
plugins_dir="$(mount_source /CLIProxyAPI/plugins)"
logs_dir="$(mount_source /CLIProxyAPI/logs)"
[[ -n "$plugins_dir" && -d "$plugins_dir" ]] || die "container $container has no host directory mounted at /CLIProxyAPI/plugins"
image="$(docker inspect -f '{{.Config.Image}}' "$container")"
arch="$(docker image inspect -f '{{.Architecture}}' "$(docker inspect -f '{{.Image}}' "$container")")"
case "$arch" in amd64|arm64) ;; *) die "unsupported architecture: $arch" ;; esac

log "container: $container ($image, linux/$arch)"
log "config:    $config_file"
log "plugins:   $plugins_dir"

# plugin_id <file>: the plugin ID cpa derives from a library file name. The
# plugin store installs versioned files such as cpa-key-billing-v1.3.18.so,
# which cpa reads as ID cpa-key-billing, version 1.3.18.
plugin_id() {
  local name
  name="$(basename "$1" .so)"
  if [[ "$name" =~ ^(.+)-v([0-9][0-9A-Za-z.+-]*)$ ]]; then
    echo "${BASH_REMATCH[1]}"
  else
    echo "$name"
  fi
}

# existing_libs <id>: libraries cpa would treat as plugin <id>, plain or
# store-versioned. They are replaced in place under their current names: cpa
# prefers versioned files and the plugin store may pin a version in config, so
# a new file with a different name could be ignored or deleted by cpa.
existing_libs() {
  find "$plugins_dir" -name "$1*.so" -type f | sort | while IFS= read -r lib; do
    [[ "$(plugin_id "$lib")" == "$1" ]] && echo "$lib"
  done
}
mapfile -t existing_kb < <(existing_libs cpa-key-billing)
mapfile -t existing_og < <(existing_libs orangeguard)

[[ -n "$config_file" && -f "$config_file" ]] || die "config.yaml mount not found for $container"
grep -qE '^plugins:' "$config_file" || die "$config_file has no plugins: section"
og_config="$(plugin_config_state "$config_file" orangeguard)"
[[ "$mode" == "rollback" ]] || case "$og_config" in
  enabled) log "config: plugins.configs.orangeguard already enabled" ;;
  missing) log "config: will add plugins.configs.orangeguard (enabled: true)" ;;
  disabled) die "plugins.configs.orangeguard exists but is not enabled: true; enable it in $config_file and re-run" ;;
  *) die "cannot safely edit plugins.configs in $config_file (not a block mapping); add 'orangeguard: {enabled: true}' under plugins.configs manually and re-run" ;;
esac

restart_container() {
  docker start "$container" >/dev/null
}

# --- rollback ---------------------------------------------------------------
restore_from() {
  local backup="$1"
  [[ -f "$backup/manifest" ]] || die "$backup/manifest not found"
  log "restoring plugin libraries from $backup"
  docker stop -t 30 "$container" >/dev/null
  while IFS=$'\t' read -r action path; do
    case "$action" in
      added) rm -f "$path"; echo "  removed $path" ;;
      replaced) cat "$backup/files$path" >"$path"; echo "  restored $path" ;;
    esac
  done <"$backup/manifest"
  restart_container
  log "container started with the previous plugins"
}

if [[ "$mode" == "rollback" ]]; then
  restore_from "$rollback_dir"
  exit 0
fi

# New libraries go next to an existing plugin, else into plugins/linux/<arch>.
target_dir="$plugins_dir/linux/$arch"
if (( ${#existing_og[@]} > 0 )); then
  target_dir="$(dirname "${existing_og[0]}")"
elif (( ${#existing_kb[@]} > 0 )); then
  target_dir="$(dirname "${existing_kb[0]}")"
fi
if (( ${#existing_og[@]} > 0 )); then
  og_targets=("${existing_og[@]}")
  log "existing orangeguard (replaced in place): ${existing_og[*]}"
else
  og_targets=("$target_dir/orangeguard.so")
fi
kb_targets=()
if [[ -n "$with_kb" ]]; then
  if (( ${#existing_kb[@]} > 0 )); then
    kb_targets=("${existing_kb[@]}")
    log "key-billing fork replaces (in place): ${existing_kb[*]}"
  else
    kb_targets=("$target_dir/cpa-key-billing.so")
  fi
elif (( ${#existing_kb[@]} > 0 )); then
  log "key-billing left untouched: ${existing_kb[*]}"
fi
log "install target: $target_dir"

# --- 2. build ---------------------------------------------------------------
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
log "building in $GO_IMAGE (linux/$arch)"
# Servers behind an HTTP proxy: HTTPS_PROXY/NO_PROXY are passed through, and
# BUILD_CA_BUNDLE can point at an extra CA bundle the proxy needs.
build_args=()
for var in HTTPS_PROXY HTTP_PROXY NO_PROXY https_proxy http_proxy no_proxy; do
  [[ -n "${!var:-}" ]] && build_args+=(-e "$var=${!var}")
done
if [[ -n "${BUILD_CA_BUNDLE:-}" ]]; then
  build_args+=(-v "$BUILD_CA_BUNDLE:/etc/ssl/certs/build-ca.crt:ro"
    -e SSL_CERT_FILE=/etc/ssl/certs/build-ca.crt -e GIT_SSL_CAINFO=/etc/ssl/certs/build-ca.crt)
fi
docker run --rm --network host --platform "linux/$arch" -v "$work:/out" "${build_args[@]}" \
  -e GOFLAGS=-buildvcs=false -e GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}" \
  -e OG_REPO="$ORANGEGUARD_REPO" -e OG_REF="$ORANGEGUARD_REF" \
  -e KB_REPO="$KEYBILLING_REPO" -e KB_REF="$KEYBILLING_REF" -e WITH_KB="$with_kb" \
  "$GO_IMAGE" bash -euc '
    git clone -q --depth 1 -b "$OG_REF" "$OG_REPO" /src/og
    og_rev="$(git -C /src/og rev-parse --short HEAD)"
    (cd /src/og && CGO_ENABLED=1 go build -buildmode=c-shared \
      -ldflags "-X main.pluginVersion=$OG_REF-$og_rev" -o /out/orangeguard.so .)
    printf "orangeguard %s\n" "$og_rev" >/out/revisions
    if [[ -n "$WITH_KB" ]]; then
      git clone -q --depth 1 -b "$KB_REF" "$KB_REPO" /src/kb
      kb_rev="$(git -C /src/kb rev-parse --short HEAD)"
      (cd /src/kb && CGO_ENABLED=1 go build -tags cshared -buildmode=c-shared \
        -o /out/cpa-key-billing.so ./cmd/cpa-key-billing)
      printf "cpa-key-billing %s\n" "$kb_rev" >>/out/revisions
    fi
    rm -f /out/*.h
    chown -R '"$(id -u):$(id -g)"' /out'
sed 's/^/    /' "$work/revisions"

if [[ "$mode" == "dry-run" ]]; then
  ls -la "$work"/*.so
  log "dry run: nothing installed"
  exit 0
fi

# --- 3. back up and install ---------------------------------------------------
backup="$CPA_DIR/backups/plugins-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup/files"
: >"$backup/manifest"
backup_file() { # path
  mkdir -p "$backup/files$(dirname "$1")"
  cp -a "$1" "$backup/files$1"
}

log "stopping $container"
docker stop -t 30 "$container" >/dev/null
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

backup_file "$config_file"
if [[ "$og_config" == "missing" ]]; then
  add_plugin_config "$config_file" orangeguard || die "failed to edit $config_file"
  printf 'replaced\t%s\n' "$config_file" >>"$backup/manifest"
  [[ "$(plugin_config_state "$config_file" orangeguard)" == "enabled" ]] || {
    cat "$backup/files$config_file" >"$config_file"
    die "config edit did not produce an enabled orangeguard entry; config restored"
  }
  echo "  added plugins.configs.orangeguard to $config_file"
fi
if [[ -n "$with_kb" ]]; then
  # Billing database (default plugins/cpa-key-billing-state-v1.db) and its WAL.
  while IFS= read -r db; do backup_file "$db"; done < <(find "$plugins_dir" -maxdepth 3 -name 'cpa-key-billing-state*' -type f)
fi

mkdir -p "$target_dir"
install_lib() { # built-file destination
  if [[ -f "$2" ]]; then
    backup_file "$2"
    printf 'replaced\t%s\n' "$2" >>"$backup/manifest"
  else
    printf 'added\t%s\n' "$2" >>"$backup/manifest"
  fi
  install -m 0755 "$1" "$2"
  echo "  installed $2"
}
# Every copy is replaced, so no location can still load an old build.
for lib in "${og_targets[@]}"; do
  mkdir -p "$(dirname "$lib")"
  install_lib "$work/orangeguard.so" "$lib"
done
for lib in "${kb_targets[@]}"; do
  install_lib "$work/cpa-key-billing.so" "$lib"
done
cp "$work/revisions" "$backup/revisions"
log "backup: $backup"

log "starting $container"
restart_container

# --- 4. verify ----------------------------------------------------------------
cpa_log() {
  docker logs --since "$started_at" "$container" 2>&1
  if [[ -n "$logs_dir" && -d "$logs_dir" ]]; then
    find "$logs_dir" -maxdepth 1 -type f -newermt "$started_at" -exec cat {} + 2>/dev/null || true
  fi
}
# orangeguard must register; so must key-billing whenever it was installed
# before or by this run, since the restart reloads it too.
expect_kb=""
(( ${#existing_kb[@]} > 0 || ${#kb_targets[@]} > 0 )) && expect_kb=1
ok=""
for ((i = 0; i < VERIFY_TIMEOUT; i++)); do
  out="$(cpa_log)"
  if grep -q 'plugin registered plugin_id=orangeguard' <<<"$out" &&
    { [[ -z "$expect_kb" ]] || grep -q 'plugin registered plugin_id=cpa-key-billing' <<<"$out"; }; then
    ok=1
    break
  fi
  if grep -qiE 'plugin_id=(orangeguard|cpa-key-billing).*(fail|error)|(fail|error).*plugin_id=(orangeguard|cpa-key-billing)' <<<"$out"; then
    break
  fi
  [[ "$(docker inspect -f '{{.State.Running}}' "$container")" == "true" ]] || break
  sleep 1
done

if [[ -z "$ok" ]]; then
  echo "---- CPA log since start ----" >&2
  cpa_log | grep -iE 'plugin|fatal|panic' | tail -30 >&2 || true
  echo "-----------------------------" >&2
  log "plugins did not load; rolling back"
  restore_from "$backup"
  die "installation rolled back; see the log above"
fi

cpa_log | grep -E 'plugin registered plugin_id=(orangeguard|cpa-key-billing)' | sed 's/^/    /'
log "done. Plugins are loaded."
echo
echo "Next steps:"
echo "  * orangeguard is inactive until you add rules under plugins.configs.orangeguard"
echo "    in $config_file (see config.example.yaml in the OrangeGuard repo)."
if [[ -n "$with_kb" ]]; then
  echo "  * key-billing fork: unpriced models are admitted at \$0 and models.dev prices"
  echo "    refresh daily. Do not click \"update\" for it in the CPA plugin store; re-run"
  echo "    this script with --with-key-billing instead."
elif (( ${#existing_kb[@]} > 0 )); then
  echo "  * cpa-key-billing refuses models without a price, including virtual model"
  echo "    names. Give every virtual model a custom price (0 is fine: usage is billed"
  echo "    at the price of the member that served it) and price every member."
fi
echo "  * Roll back with:  sudo bash $0 --rollback $backup"
