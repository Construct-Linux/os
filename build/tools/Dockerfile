# check=skip=InvalidDefaultArgInFrom
# What cmd/image usb runs in: spin-ami's tools image's programs - systemd's (sysusers, tmpfiles,
# hwdb, repart, ukify, systemd-measure), EROFS, dm-verity, the ESP's FAT - without what only the
# AMI needs. spin-ami's versions.yaml: wolfi-base, handed in by task tools. systemd comes from this
# repository's packages (recipes/systemd): Wolfi's is built without TPM2, and ukify computes the
# kernel image's PCR 11 policy with systemd-measure, which only a TPM2 build has, and which loads
# tpm2-tss's libraries to compute it.
ARG WOLFI_BASE_IMAGE

FROM ${WOLFI_BASE_IMAGE}
RUN --mount=type=bind,from=packages,target=/packages \
    --mount=type=bind,from=keys,target=/keys \
    cp /keys/melange.rsa.pub /etc/apk/keys/ && \
    apk add --no-cache --repository /packages \
        bash coreutils gnutar cpio util-linux mount umount findutils xz mtools \
        systemd udev systemd-ukify systemd-boot e2fsprogs dosfstools mtools \
        erofs-utils cryptsetup zstd tpm2-tss && \
    rm /etc/apk/keys/melange.rsa.pub && \
    /usr/lib/systemd/systemd-measure --version >/dev/null
ENTRYPOINT ["/bin/bash"]
