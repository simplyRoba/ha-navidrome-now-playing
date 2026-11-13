#!/bin/sh
# Export a package to the host; never install it into a Navidrome container.
set -eu
fail() { printf '%s\n' "Error: $*" >&2; exit 1; }
[ "$#" -le 1 ] || fail 'Usage: build.sh [HOST_OUTPUT_DIRECTORY]'
command -v docker >/dev/null 2>&1 || fail 'Docker CLI is required'
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
output=${1-"$repo/build/navidrome-plugin"}
case "$output" in
    ''|-*|*','*|*'
'*|*'	'*) fail 'Output directory must be nonempty and must not contain commas, tabs or newlines, or start with a dash' ;;
esac
case "$output" in /*) ;; *) output=$(pwd -P)/$output ;; esac
case "$output" in
    /|*/../*|*/..|*/./*|*/.|*//*|*/) fail 'Output directory must not be root or contain dot components, repeated or trailing slashes' ;;
esac
# Check every component, including dangling symlinks, before mkdir or export.
check_directory() {
    remaining=${output#/}
    current=
    while [ -n "$remaining" ]; do
        part=${remaining%%/*}
        current=$current/$part
        [ ! -L "$current" ] || fail 'Output directory must not contain symlinks'
        if [ -e "$current" ]; then
            [ -d "$current" ] || fail 'Output path components must be directories'
        fi
        case "$remaining" in */*) remaining=${remaining#*/} ;; *) remaining= ;; esac
    done
}
check_directory
(umask 022; mkdir -p "$output")
# The directory bind mount and final package must be readable by non-root Navidrome.
chmod a+rx "$output"
[ -r "$output" ] && [ -w "$output" ] && [ -x "$output" ] || fail 'Output directory must be readable, writable and searchable'
localtemp=
lock=$output/.build.lock
lock_owned=
cleanup() {
    status=$?
    trap - 0 HUP INT TERM
    if [ -n "$localtemp" ]; then rm -rf -- "$localtemp"; fi
    if [ -n "$lock_owned" ]; then rmdir -- "$lock"; fi
    exit "$status"
}
trap cleanup 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
if (umask 077; mkdir "$lock") 2>/dev/null; then
    lock_owned=yes
else
    fail "Already building or cannot acquire build lock: $lock"
fi
directory_state=$(ls -di "$output")
artifact=$output/navidrome-nowplaying.ndp
artifact_state() {
    [ ! -L "$artifact" ] || fail 'Package destination must not be a symlink'
    if [ -e "$artifact" ]; then
        [ -f "$artifact" ] || fail 'Package destination must be a regular file'
        ls -lid "$artifact"
        cksum "$artifact"
    else
        printf '%s\n' absent
    fi
}
before=$(artifact_state) || exit 1
# Claim a private name without a host mktemp dependency. Never remove others' temps.
n=0
while :; do
    candidate=$output/.build-$$-$n
    if (umask 077; mkdir "$candidate") 2>/dev/null; then localtemp=$candidate; break; fi
    n=$((n + 1))
    [ "$n" -lt 100 ] || fail 'Cannot create temporary build directory'
done
docker build -f "$script_dir/Dockerfile" --output "type=local,dest=$localtemp/export" "$repo"
package=$localtemp/export/navidrome-nowplaying.ndp
[ ! -L "$localtemp/export" ] && [ -d "$localtemp/export" ] || fail 'Docker export directory is missing or unsafe'
[ ! -L "$package" ] && [ -f "$package" ] && [ -s "$package" ] || fail 'Docker export must contain a nonempty regular package'
chmod 644 "$package"
check_directory
[ "$(ls -di "$output")" = "$directory_state" ] || fail 'Output directory changed during build'
after=$(artifact_state) || exit 1
[ "$after" = "$before" ] || fail 'Package destination changed during build'
# Rename only the package, on the same filesystem; keep the bind-mounted directory inode.
mv -f -- "$package" "$artifact"
printf 'Built %s\n' "$artifact"
