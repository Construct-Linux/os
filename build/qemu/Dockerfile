# check=skip=InvalidDefaultArgInFrom
# The harness's QEMU: QEMU itself, built with CanoKey - a whole FIDO2 security key (PIN,
# hmac-secret) in emulation, which test:lifecycle opens /var with. Distributions build QEMU without
# it (libcanokey-qemu is a test library), and USB passthrough of a host key carries U2F only, not
# FIDO2. x86_64 with KVM, user networking, vhost-vsock, a TPM's socket and VNC: what cmd/image's
# machines use, nothing else. On Debian, as canokey's own CI builds it: its mbedTLS generates
# sources with Python's SHA-224, which Wolfi's Python is built without. A tool of the harness's;
# nothing of it reaches the image. build/versions.env pins Debian, QEMU and canokey-qemu.
ARG HARNESS_DEBIAN_IMAGE

FROM ${HARNESS_DEBIAN_IMAGE} AS build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake git ninja-build \
        pkg-config python3 python3-venv python3-jsonschema python3-jinja2 flex bison xz-utils curl ca-certificates \
        libglib2.0-dev libpixman-1-dev libslirp-dev zlib1g-dev libcap-ng-dev libseccomp-dev
ARG CANOKEY_QEMU_COMMIT
RUN git clone https://github.com/canokeys/canokey-qemu /src/canokey && \
    cd /src/canokey && git checkout -q "${CANOKEY_QEMU_COMMIT:?}" && git submodule update -q --init --recursive && \
    cmake -S . -B build -G Ninja -DCMAKE_INSTALL_PREFIX=/opt/qemu -DCMAKE_BUILD_TYPE=Release && \
    cmake --build build && cmake --install build
ARG QEMU_VERSION
ARG QEMU_SHA256
RUN curl -fsSL -o /src/qemu.tar.xz "https://download.qemu.org/qemu-${QEMU_VERSION:?}.tar.xz" && \
    echo "${QEMU_SHA256:?}  /src/qemu.tar.xz" | sha256sum -c - && \
    tar -xJf /src/qemu.tar.xz -C /src
RUN cd /src/qemu-${QEMU_VERSION} && \
    PKG_CONFIG_PATH=/opt/qemu/lib/pkgconfig:/opt/qemu/lib/x86_64-linux-gnu/pkgconfig ./configure --prefix=/opt/qemu \
        --extra-cflags=-I/opt/qemu/include --extra-ldflags="-L/opt/qemu/lib -L/opt/qemu/lib/x86_64-linux-gnu" \
        --target-list=x86_64-softmmu --enable-kvm --enable-slirp --enable-canokey --enable-tpm --enable-vnc \
        --enable-seccomp --disable-docs --disable-werror --disable-gtk --disable-sdl --disable-opengl \
        --disable-pa --disable-alsa --disable-pipewire --disable-jack && \
    make -j"$(nproc)" && make install && \
    LD_LIBRARY_PATH=/opt/qemu/lib:/opt/qemu/lib/x86_64-linux-gnu /opt/qemu/bin/qemu-system-x86_64 -device help | grep -q '"canokey"'

# swtpm beside it: QEMU's TPM emulator is handed the TPM's data socket over the control socket
# (SCM_RIGHTS), which does not cross from a host's swtpm into this container. With SWTPM_STATE set,
# the entry point starts it there first, its socket SWTPM_STATE/swtpm.sock.
FROM ${HARNESS_DEBIAN_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends libglib2.0-0t64 libpixman-1-0 libslirp0 \
        zlib1g libcap-ng0 libseccomp2 swtpm swtpm-tools && rm -rf /var/lib/apt/lists/*
COPY --from=build /opt/qemu /opt/qemu
ENV LD_LIBRARY_PATH=/opt/qemu/lib:/opt/qemu/lib/x86_64-linux-gnu
COPY qemu.sh /opt/qemu/qemu.sh
ENTRYPOINT ["/opt/qemu/qemu.sh"]
