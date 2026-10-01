# check=skip=InvalidDefaultArgInFrom
# The desktop kernel: spin-ami's kernel build - the same toolchain, tarball and assertions - with
# kernel/desktop.config merged over spin-ami's host.config and metal.config, and modules built,
# signed and installed beside the bzImage.
#
# spin-ami's fragments come in as the build context "ami-kernel" (task kernel), not as copies, so
# what a desktop adds stays what this file's fragment says and nothing drifts underneath it.
# spin-ami's versions.yaml: debian, debian-snapshot and kernel, handed in by task kernel.
ARG DEBIAN_IMAGE
ARG DEBIAN_SNAPSHOT

FROM ${DEBIAN_IMAGE} AS build-base

ARG DEBIAN_SNAPSHOT
ENV DEBIAN_FRONTEND=noninteractive

RUN echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache

# The archive as it stood on DEBIAN_SNAPSHOT. Valid-Until is not checked because a snapshot's
# Release files expire by design; the signatures still are, which is what makes plain http
# enough - and it has to be http, since the image has no CA certificates until it installs them
# from this archive.
RUN rm -f /etc/apt/sources.list.d/debian.sources && cat > /etc/apt/sources.list.d/snapshot.sources <<EOF
Types: deb
URIs: http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}
Suites: trixie trixie-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
Check-Valid-Until: no

Types: deb
URIs: http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}
Suites: trixie-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
Check-Valid-Until: no
EOF

RUN --mount=type=cache,sharing=locked,id=desktop-kernel-aptlib,target=/var/lib/apt \
    --mount=type=cache,sharing=locked,id=desktop-kernel-aptcache,target=/var/cache/apt \
    apt-get update && apt-get install -y --no-install-recommends \
        build-essential libncurses-dev flex bison libssl-dev libelf-dev bc cpio kmod \
        wget xz-utils ca-certificates file dwarves python3 \
        clang-19 lld-19 llvm-19

# Every make below - the configuration included, since which options exist depends on the
# compiler it finds - uses clang-19, ld.lld-19 and llvm-*-19 (Documentation/kbuild/llvm.rst).
ENV LLVM=-19

ARG KERNEL_VERSION
# The tarball, by content: https says who sent it, not what they sent, and this is the one
# input that comes from outside the repository.
ARG KERNEL_SHA256

WORKDIR /usr/src

RUN --mount=type=cache,sharing=locked,id=ami-kernel-src-${KERNEL_VERSION},target=/var/cache/kernel \
    major="$(echo "${KERNEL_VERSION}" | cut -d. -f1)" && \
    tarball="/var/cache/kernel/linux-${KERNEL_VERSION}.tar.xz" && \
    if [ ! -f "$tarball" ]; then \
        wget -O "$tarball" "https://cdn.kernel.org/pub/linux/kernel/v${major}.x/linux-${KERNEL_VERSION}.tar.xz"; \
    fi && \
    # Every build, not only after a download: the cache mount outlives the build that filled it.
    echo "${KERNEL_SHA256}  $tarball" | sha256sum -c - && \
    tar -xf "$tarball" -C /usr/src && \
    mv linux-${KERNEL_VERSION} linux

COPY --from=ami-kernel host.config metal.config /usr/src/fragments/
COPY kernel/desktop.config /usr/src/fragments/

# Which kernel this is: the fragments of kernel/ merged over the defconfigs, in order, a later
# one's line for a symbol replacing an earlier one's - host.config for EC2's, host.config then
# metal.config for bare metal's (internal/prebuilt gives each input its own).
ARG KERNEL_FRAGMENTS="host.config metal.config desktop.config"

RUN <<EOT
    set -eu
    cd /usr/src/linux
    # What the fragments promise, merged by the kernel's own tool, so a line metal.config
    # overrides is the one asserted instead of host.config's.
    # realpath -e because merge_config.sh makes an empty base of a file that is not there.
    fragments=$(cd /usr/src/fragments && realpath -e ${KERNEL_FRAGMENTS:?})
    mkdir /usr/src/promised
    scripts/kconfig/merge_config.sh -m -O /usr/src/promised $fragments
    promised=/usr/src/promised/.config
    make x86_64_defconfig
    scripts/kconfig/merge_config.sh -m .config kernel/configs/kvm_guest.config "$promised"
    # Every ethernet vendor but the ones a fragment turns on: an instance has an ENA and nothing
    # else, and a list of seventy vendors to switch off by name would be one that drifts with
    # every kernel. (virtio-net, which hack/boot's QEMU uses, is not under a vendor.)
    for v in $(sed -n 's/^\(CONFIG_NET_VENDOR_[A-Z0-9_]*\)=y$/\1/p' .config); do
        grep -qxF "$v=y" "$promised" || scripts/config --disable "$v"
    done
    make olddefconfig

    # A symbol olddefconfig dropped is one whose dependency is missing, and the kernel it leaves
    # boots and lacks something - which shows up later as something other than a missing option.
    fail=0
    while IFS= read -r line; do
        case "$line" in
        CONFIG_*=*)
            grep -qxF "$line" .config || { echo "${KERNEL_FRAGMENTS}: $line did not survive olddefconfig: $(grep "^${line%%=*}=" .config || echo unset)"; fail=1; } ;;
        "# CONFIG_"*" is not set")
            sym=${line#\# }; sym=${sym%% *}
            ! grep -q "^${sym}=" .config || { echo "${KERNEL_FRAGMENTS}: $sym is set: $(grep "^${sym}=" .config)"; fail=1; } ;;
        esac
    done < "$promised"
    # A vendor some select turned back on is drivers nobody asked for.
    if extra=$(grep '^CONFIG_NET_VENDOR_.*=y$' .config | grep -vxF -f "$promised"); then
        echo "ethernet vendors no fragment turns on:"; echo "$extra"; fail=1
    fi
    exit $fail
EOT

FROM build-base AS build

ARG DEBIAN_SNAPSHOT
ARG KERNEL_NPROC=16

# The build's date, user and host go into the banner (uname -v) unless told otherwise; they are
# the snapshot's day and fixed names. The module signing key is not: make generates it in this
# tree (certs/signing_key.pem) and it never leaves the stage, so only these modules ever load.
RUN cd linux && make -j${KERNEL_NPROC} \
        KBUILD_BUILD_TIMESTAMP="$(date -u -d "$(echo "${DEBIAN_SNAPSHOT}" | cut -c1-8)")" \
        KBUILD_BUILD_USER=spin-desktop KBUILD_BUILD_HOST=build \
        bzImage modules

RUN <<EOT
    set -eu
    cd linux
    mkdir -p /out/kernel /out/modules
    cp arch/x86/boot/bzImage /out/kernel/bzImage
    cp .config /out/kernel/config
    make -s kernelrelease > /out/kernel/release
    make -s INSTALL_MOD_PATH=/out/modules/usr INSTALL_MOD_STRIP=1 modules_install
    rm -f /out/modules/usr/lib/modules/*/build
EOT

FROM scratch AS kernel
COPY --from=build /out/ /
