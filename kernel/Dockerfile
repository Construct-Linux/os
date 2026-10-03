# check=skip=InvalidDefaultArgInFrom
# CONSTRUCT's kernel: kernel.org's tarball, built with Wolfi's Clang - the LLVM the rest of the
# distribution is built with - over Arch Linux's configuration for the same kernel with
# kernel/platform.config and kernel/desktop.config merged on it, every line of theirs asserted,
# and modules built, signed and installed beside the bzImage. build/versions.env pins the inputs:
# wolfi-base and kernel, handed in by task kernel.
ARG WOLFI_BASE_IMAGE

FROM ${WOLFI_BASE_IMAGE} AS build-base

# zstd compresses the modules as modules_install writes them (desktop.config's MODULE_COMPRESS_ZSTD);
# pahole makes BTF; libcap is turbostat's.
RUN apk add --no-cache build-base bash bc bison flex perl python-3 elfutils-dev openssl-dev \
        ncurses-dev openssl pahole cpio kmod xz zstd curl coreutils findutils diffutils gnutar patch libcap-dev \
        clang-23 lld-23 llvm-23

# Every make below - the configuration included, since which options exist depends on the
# compiler it finds - uses clang, ld.lld and llvm-* (Documentation/kbuild/llvm.rst).
ENV LLVM=1

ARG KERNEL_VERSION
# The tarball, by content: https says who sent it, not what they sent.
ARG KERNEL_SHA256

WORKDIR /usr/src

RUN --mount=type=cache,sharing=locked,id=construct-kernel-src-${KERNEL_VERSION},target=/var/cache/kernel \
    major="$(echo "${KERNEL_VERSION}" | cut -d. -f1)" && \
    tarball="/var/cache/kernel/linux-${KERNEL_VERSION}.tar.xz" && \
    if [ ! -f "$tarball" ]; then \
        curl -fsSL -o "$tarball" "https://cdn.kernel.org/pub/linux/kernel/v${major}.x/linux-${KERNEL_VERSION}.tar.xz"; \
    fi && \
    # Every build, not only after a download: the cache mount outlives the build that filled it.
    echo "${KERNEL_SHA256}  $tarball" | sha256sum -c - && \
    tar -xf "$tarball" -C /usr/src && \
    mv linux-${KERNEL_VERSION} linux

# Backports for hardware the pinned kernel does not run yet, each a commit of mainline or of a
# maintainer's tree, or a fix posted upstream that is carried until it lands - its own header
# says which, and why. --forward: once the pinned kernel carries
# a patch it no longer applies and the build stops, so it is dropped. --fuzz=0: a patch that no
# longer fits exactly stops it too, instead of landing somewhere else.
COPY kernel/patches/ /usr/src/patches/
RUN cd /usr/src/linux && for p in /usr/src/patches/*.patch; do \
        echo "applying $p"; patch -p1 --fuzz=0 --forward --no-backup-if-mismatch < "$p" || exit 1; \
    done

COPY kernel/platform.config kernel/desktop.config /usr/src/fragments/

# The base the fragments are merged over: Arch Linux's configuration for this same kernel, every
# driver a general-purpose distribution builds, as modules. A desktop meets hardware nobody listed
# when the image was built; a module costs nothing on a machine without its device, while a driver
# left out is a device that does not work. Pinned by tag and content, like the tarball.
ADD --checksum=sha256:ff668413582a831e0ec872852b42570278cf3d5b3b394b4de13cf6b9119ad04e \
    https://gitlab.archlinux.org/archlinux/packaging/packages/linux/-/raw/7.2.8.arch1-2/config.x86_64 \
    /usr/src/base.config

# Which kernel this is: the fragments merged over the base, in order, a later one's line for a
# symbol replacing an earlier one's.
ARG KERNEL_FRAGMENTS="platform.config desktop.config"

RUN <<EOT
    set -eu
    cd /usr/src/linux
    # What the fragments promise, merged by the kernel's own tool, so a line desktop.config
    # overrides is the one asserted instead of platform.config's.
    # realpath -e because merge_config.sh makes an empty base of a file that is not there.
    fragments=$(cd /usr/src/fragments && realpath -e ${KERNEL_FRAGMENTS:?})
    mkdir /usr/src/promised
    scripts/kconfig/merge_config.sh -m -O /usr/src/promised $fragments
    promised=/usr/src/promised/.config
    cp /usr/src/base.config .config
    scripts/kconfig/merge_config.sh -m .config "$promised"
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
    exit $fail
EOT

FROM build-base AS build

ARG KERNEL_NPROC=16
# The build's date (task kernel passes the last commit's), user and host go into the banner
# (uname -v) unless told otherwise.
ARG KBUILD_BUILD_TIMESTAMP

# An AutoFDO profile of the kernel at work on a real machine (task kernel:profile writes it from
# what construct kernel-profile recorded): Clang lays out and inlines the code that profile shows
# hot. Without one, the kernel is built the same, unprofiled.
COPY kernel/profiles/ /usr/src/profiles/

# The build's date, user and host are fixed, so the same sources make the same banner. The module
# signing key is not: make generates it in this tree (certs/signing_key.pem) and it never leaves the
# stage, so only these modules ever load. x86-64-v3 (AVX2, BMI2, 2013 on) is the CPU level
# everything construct builds targets.
RUN cd linux && \
    profile=""; [ ! -f /usr/src/profiles/autofdo.afdo ] || profile="CLANG_AUTOFDO_PROFILE=/usr/src/profiles/autofdo.afdo"; \
    make -j${KERNEL_NPROC} $profile \
        KBUILD_BUILD_TIMESTAMP="${KBUILD_BUILD_TIMESTAMP:?}" \
        KBUILD_BUILD_USER=construct KBUILD_BUILD_HOST=build \
        KCFLAGS="-march=x86-64-v3" \
        bzImage modules

RUN <<EOT
    set -eu
    cd linux
    mkdir -p /out/kernel /out/modules
    cp arch/x86/boot/bzImage /out/kernel/bzImage
    cp .config /out/kernel/config
    # The kernel with its symbols, which a profile recorded on a machine is resolved against
    # (llvm-profgen, task kernel:profile). Not in the image.
    cp vmlinux /out/kernel/vmlinux
    make -s kernelrelease > /out/kernel/release
    make -s INSTALL_MOD_PATH=/out/modules/usr INSTALL_MOD_STRIP=1 modules_install
    rm -f /out/modules/usr/lib/modules/*/build
EOT

# turbostat, from this kernel's own tree, beside its modules: what the CPU's package and core
# power states and RAPL's energy counters say, for measuring a laptop's power (construct power).
# Wolfi has none, and it reads the MSRs and sysfs of the kernel it was built with.
RUN make -s -C linux/tools/power/x86/turbostat && \
    install -Dm755 linux/tools/power/x86/turbostat/turbostat /out/modules/usr/bin/turbostat

# perf, from the same tree: what records the kernel's taken branches on a machine for its AutoFDO
# profile (construct kernel-profile). Only what recording needs - no scripting, no TUI. Its event
# tables (jevents), which name BR_INST_RETIRED, are generated by the python python3-config names:
# perf's own need, so installed here.
RUN apk add --no-cache python-3-dev && \
    make -s -C linux/tools/perf NO_LIBPYTHON=1 NO_LIBPERL=1 NO_SLANG=1 NO_GTK2=1 NO_JVMTI=1 \
        NO_LIBBABELTRACE=1 NO_LIBUNWIND=1 NO_LIBNUMA=1 NO_SDT=1 NO_LIBTRACEEVENT=1 NO_LIBLLVM=1 \
        BUILD_BPF_SKEL=0 && \
    install -Dm755 linux/tools/perf/perf /out/modules/usr/bin/perf

FROM scratch AS kernel
COPY --from=build /out/ /
