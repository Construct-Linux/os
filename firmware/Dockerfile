# check=skip=InvalidDefaultArgInFrom
# The firmware a desktop's drivers ask for as their modules probe, from the root's
# /usr/lib/firmware: linux-firmware's and Intel's microcode releases (build/versions.env), each
# tarball checked by its sha256, and of linux-firmware everything a PC can have: a driver is there for any
# hardware (kernel/Dockerfile's base is a general distribution's), and a driver without its
# firmware is a device that does not work.
#
# build/versions.env: wolfi-base, linux-firmware and intel-microcode, handed in by task firmware.
ARG WOLFI_BASE_IMAGE

FROM ${WOLFI_BASE_IMAGE} AS build

# coreutils: copy-firmware.sh resolves WHENCE's links with GNU realpath -m -s; busybox's has neither.
RUN apk add --no-cache bash coreutils curl gnutar xz zstd

ARG LINUX_FIRMWARE_VERSION
ARG LINUX_FIRMWARE_SHA256
ARG INTEL_MICROCODE_VERSION
ARG INTEL_MICROCODE_SHA256

# Checked at every build, not only after a download: the cache mount outlives the build that
# filled it. The tarball is linux-firmware's source tree; its copy-firmware.sh installs it as
# drivers look for it, zstd-compressed (desktop.config's FW_LOADER_COMPRESS_ZSTD), with the
# links WHENCE names (iwlwifi-*.ucode at the top, Intel's
# Bluetooth under the names each part asks for). Of that, all but what no PC carries - which is
# half of its two gigabytes: Qualcomm's ARM SoCs (qcom), other ARM boards' parts (dpaa2, amlogic,
# arm, NVIDIA's Tegra) and data-centre network cards (Netronome, Mellanox, Marvell's Prestera
# switches, QLogic, Chelsio, Cavium's LiquidIO, Broadcom's NetXtreme II). AMD's microcode, and
# Intel's from its own release, go uncompressed, since the kernel reads them from an early cpio
# before it can decompress anything. The regulatory
# database is not here: wireless-regdb is its own package. WHENCE and the licenses say what each
# file is redistributed under.
RUN --mount=type=cache,sharing=locked,id=desktop-firmware,target=/var/cache/firmware \
    set -eu; cd /var/cache/firmware; \
    lf="linux-firmware-${LINUX_FIRMWARE_VERSION}.tar.xz"; \
    [ -f "$lf" ] || curl -fsSL -o "$lf" "https://cdn.kernel.org/pub/linux/kernel/firmware/$lf"; \
    echo "${LINUX_FIRMWARE_SHA256}  $lf" | sha256sum -c -; \
    iu="intel-microcode-${INTEL_MICROCODE_VERSION}.tar.gz"; \
    [ -f "$iu" ] || curl -fsSL -o "$iu" "https://github.com/intel/Intel-Linux-Processor-Microcode-Data-Files/archive/refs/tags/${INTEL_MICROCODE_VERSION}.tar.gz"; \
    echo "${INTEL_MICROCODE_SHA256}  $iu" | sha256sum -c -; \
    mkdir -p /lf /fw /src; \
    tar -xJf "$lf" -C /lf --strip-components=1; \
    cd /lf && ./copy-firmware.sh -j"$(nproc)" --zstd /fw >/dev/null; \
    cd /fw; \
    rm -rf qcom dpaa2 amlogic arm nvidia/tegra* netronome mellanox mrvl/prestera qed \
      ql2*_fw.bin.zst cxgb4 liquidio bnx2x phanfw.bin.zst amd-ucode; \
    find . -type l | while read -r l; do [ -e "$l" ] || rm "$l"; done; \
    cp -a . /src; \
    for f in intel/iwlwifi i915 xe amdgpu nvidia/ga102 ath10k ath11k brcm qca rtw89 mediatek intel/vsc cirrus; do \
      [ -d "/src/$f" ] || { echo "firmware: $f missing"; exit 1; }; done; \
    cd /lf && cp -a WHENCE LICENSES /src && cp -a --parents amd-ucode /src; \
    tar -xzf "/var/cache/firmware/$iu" -C /src --strip-components=1 --wildcards '*/intel-ucode/*' '*/license'

# Fixes linux-firmware has merged and not yet released, each a file at its commit, checked by
# its sha256, over the release's - each goes when LINUX_FIRMWARE_VERSION is a release that has it.
# - intel/ibt-00a0-01a1-pci.ddc (a98a6daa): the BE211's Bluetooth DDC ended in a stray byte, a
#   record the controller refused - "Failed to send Intel_Write_DDC (-95)" at every boot and
#   resume of a ThinkPad X9-15p, the rest of its parameters unsent.
ADD --checksum=sha256:a2df9880d1895220e9bfbb86b2e2358419c56856b5dcc2d6300713969dfd0744 \
    https://gitlab.com/kernel-firmware/linux-firmware/-/raw/a98a6daa9f4dee2f662cc8a48610287189b23499/intel/ibt-00a0-01a1-pci.ddc \
    /fixes/intel/ibt-00a0-01a1-pci.ddc
RUN set -eu; cd /fixes; \
    for f in $(find . -type f); do \
      [ -e "/src/$f.zst" ] || { echo "firmware fix: /src/$f.zst is not in the release"; exit 1; }; \
      zstd -q -19 --rm -f "$f" -o "/src/$f.zst"; \
    done

# The same files are the same tar: sorted, root's, at time zero.
RUN mkdir /out && \
    tar -C /src --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -cf /out/firmware.tar . && \
    echo "linux-firmware ${LINUX_FIRMWARE_VERSION} intel-microcode ${INTEL_MICROCODE_VERSION}" > /out/firmware.source

FROM scratch AS firmware
COPY --from=build /out/ /firmware/
