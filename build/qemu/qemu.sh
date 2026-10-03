#!/bin/sh
# QEMU, with its TPM's swtpm started first when SWTPM_STATE names its state directory.
set -eu
if [ -n "${SWTPM_STATE:-}" ]; then
    mkdir -p "$SWTPM_STATE"
    rm -f "$SWTPM_STATE/swtpm.sock"
    swtpm socket --tpm2 --tpmstate dir="$SWTPM_STATE" --ctrl type=unixio,path="$SWTPM_STATE/swtpm.sock" --terminate &
    i=0
    while [ ! -S "$SWTPM_STATE/swtpm.sock" ]; do
        i=$((i + 1))
        [ "$i" -le 100 ] || { echo "swtpm made no socket" >&2; exit 1; }
        sleep 0.05
    done
fi
exec /opt/qemu/bin/qemu-system-x86_64 "$@"
