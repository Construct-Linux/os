# check=skip=InvalidDefaultArgInFrom
# What cmd/image usb runs in: spin-ami's tools image's programs - systemd's (sysusers, tmpfiles,
# hwdb, repart, ukify), EROFS, dm-verity, the ESP's FAT - without what only the AMI needs.
# spin-ami's versions.yaml: wolfi-base, handed in by task usb.
ARG WOLFI_BASE_IMAGE

FROM ${WOLFI_BASE_IMAGE}
RUN apk add --no-cache bash coreutils gnutar cpio util-linux mount umount findutils xz mtools \
        systemd udev systemd-ukify systemd-boot e2fsprogs dosfstools mtools \
        erofs-utils cryptsetup zstd
ENTRYPOINT ["/bin/bash"]
