# check=skip=InvalidDefaultArgInFrom
# The firmware a desktop's drivers ask for as their modules probe, from the root's
# /usr/lib/firmware: spin-ami's pins of linux-firmware and intel-microcode, each tarball checked
# by its sha256, and of linux-firmware only the families kernel/desktop.config builds drivers
# for - not its six hundred megabytes.
#
# spin-ami's versions.yaml: wolfi-base, linux-firmware and intel-microcode, handed in by task firmware.
ARG WOLFI_BASE_IMAGE

FROM ${WOLFI_BASE_IMAGE} AS build

RUN apk add --no-cache bash curl gnutar xz zstd

ARG LINUX_FIRMWARE_VERSION
ARG LINUX_FIRMWARE_SHA256
ARG INTEL_MICROCODE_VERSION
ARG INTEL_MICROCODE_SHA256

# Checked at every build, not only after a download: the cache mount outlives the build that
# filled it. The tarball is linux-firmware's source tree; its copy-firmware.sh installs it as
# drivers look for it, zstd-compressed (desktop.config's FW_LOADER_COMPRESS_ZSTD), with the
# links WHENCE names (iwlwifi-*.ucode at the top, Intel's
# Bluetooth under the names each part asks for). Of that, per family: Intel graphics (i915, xe)
# and AMD's (amdgpu); Wi-Fi (iwlwifi, ath11k, ath12k, MediaTek's, rtw88, rtw89); Bluetooth
# (Intel's ibt, Realtek's; MediaTek's is under mediatek/); Realtek ethernet, USB ones included;
# Intel's AVS audio DSP; Cirrus Logic's amplifiers and codecs, tuned per laptop (the ThinkPad X9's
# among them); Intel's camera units (IPU6, IPU7), sensor hub (ISH, and laptop makers' builds of it
# under LENOVO/) and NPU; AMD's microcode, and Intel's from its own release - both uncompressed,
# since the kernel reads them from an early cpio before it can decompress anything. The regulatory
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
    cd /fw && cp -a --parents i915 xe amdgpu intel/iwlwifi iwlwifi-*.zst ath11k ath12k mediatek rtw88 rtw89 \
      intel/ibt-*.zst rtl_bt rtl_nic intel/avs cirrus intel/ipu intel/ish LENOVO intel/vpu /src; \
    cd /lf && cp -a WHENCE LICENSES /src && cp -a --parents amd-ucode /src; \
    tar -xzf "/var/cache/firmware/$iu" -C /src --strip-components=1 --wildcards '*/intel-ucode/*' '*/license'

# The same files are the same tar: sorted, root's, at time zero.
RUN mkdir /out && \
    tar -C /src --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -cf /out/firmware.tar . && \
    echo "linux-firmware ${LINUX_FIRMWARE_VERSION} intel-microcode ${INTEL_MICROCODE_VERSION}" > /out/firmware.source

FROM scratch AS firmware
COPY --from=build /out/ /firmware/
