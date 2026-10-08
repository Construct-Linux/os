# check=skip=InvalidDefaultArgInFrom
# What cmd/image usb runs in: systemd's programs (sysusers, tmpfiles, hwdb, repart, ukify,
# systemd-measure), EROFS, dm-verity, the ESP's FAT. build/versions.env: wolfi-base, handed in by
# task tools. systemd comes from this
# repository's packages (recipes/systemd): Wolfi's is built without TPM2, and ukify computes the
# kernel image's PCR 11 policy with systemd-measure, which only a TPM2 build has, and which loads
# tpm2-tss's libraries to compute it. glib-dev for gresource: what GNOME Shell's theme gresource
# holds is read with it, not guessed from its bytes.
#
# OpenSSL 4.0 at 4.0.3 or later: wolfi-base's world pins the 4.0.2 it was built with, whose
# libcrypto has no SHA-224 family, and Python's hashlib - ukify's - logged "code for hash sha224
# was not found" with a traceback at every import. Wolfi's 4.0.3 has them again; an import that
# says anything at all fails the image.
ARG WOLFI_BASE_IMAGE

FROM ${WOLFI_BASE_IMAGE}
RUN --mount=type=bind,from=packages,target=/packages \
    --mount=type=bind,from=keys,target=/keys \
    cp /keys/melange.rsa.pub /etc/apk/keys/ && \
    apk add --no-cache --repository /packages \
        bash coreutils gnutar cpio util-linux mount umount findutils xz mtools \
        systemd udev systemd-ukify systemd-boot e2fsprogs dosfstools mtools \
        erofs-utils cryptsetup zstd tpm2-tss glib-dev \
        "openssl-4.0-libcrypto>=4.0.3" "openssl-4.0-libssl>=4.0.3" && \
    rm /etc/apk/keys/melange.rsa.pub && \
    test -z "$(python3.13 -c 'import hashlib' 2>&1)" && \
    /usr/lib/systemd/systemd-measure --version >/dev/null && \
    gresource help >/dev/null
ENTRYPOINT ["/bin/bash"]
