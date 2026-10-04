# AGENTS.md - Construct

What an agent working here needs and the code cannot say: rules, their reasons, and how a change
is checked. `README.md` says what the desktop is; `task --list` is the catalogue; git is the
history. Nothing here is a changelog.

Construct is Wolfi plus what Wolfi lacks for a GNOME desktop (`recipes/`), on its own kernel,
booted the way systemd's image-based distributions boot: a unified kernel image with a
systemd initrd, a read-only EROFS root under dm-verity found by its root hash, partitions of the
Discoverable Partitions Specification, A/B slots updated by systemd-sysupdate, and systemd-boot
falling back from a release that does not come up.

- **Standards before our own code.** Before writing something, look for how systemd, or the
  image-based distributions (ParticleOS, GNOME OS, SteamOS, Fedora's), do it, and do that: a
  generator, a unit, a repart definition, a udev rule. Our own code is for what nobody does.

## The rules

- **English, always**: code, comments, commit messages, docs, unit descriptions, task
  descriptions. The person you work for may write in another language; what lands in the
  repository is English.
- **Locales are the product's, not the repository's**: the desktop speaks every language its
  packages ship. Never strip translations to save space: build with NLS, split catalogs into
  `<name>-lang` (`split/locales`), install every `-lang` package in `image/apko.yaml` - apko
  installs none by itself, and `go run ./cmd/image check-langs` (in `task rootfs`) fails the build
  when one is missing - keep `glibc-locale-*` in the image, and do not hard-code a language
  anywhere but the image's default (`image/rootfs/etc/locale.conf`).
- **Every build step is a Taskfile task.** A container run, a melange build, a disk, a test: if it
  is done twice it is a task, and the task is how it is done - by people, agents and CI alike.
  `task build -- <recipe>` builds a recipe, `task rootfs`, `task kernel`, `task firmware`,
  `task usb`, `task test`, `task test:usb`, `task test:lifecycle`. Do not run `docker`/`melange`
  /`apko` by hand for something a task does; add or fix the task instead.
- **Decisions are Go, not shell.** `cmd/image` (disks, the initrd, the harness, releases),
  `cmd/construct` (install, boot-check, the live user, the GTK 4 theme and Ghostty's seed),
  `internal/` (gpt, update, account).
  The Taskfile runs steps and containers, in short blocks; a unit's `ExecStart=` stays a command,
  not a program.
- **Keep looking for performance and for security**, and measure both. Every change is a chance
  to ask whether something can be faster (boot time, memory, the compiler's work) or smaller in
  what it exposes (a service that need not run, a capability not needed, a package not used). Say
  what you measured; an optimisation without a number, or a hardening without a check that it
  holds, is a guess. Open questions worth chasing are listed under "Where to look next".
- **Comments say why.** Not what the line does, never what it used to be: git keeps history.
- **Commits are partial and each one builds.** Subject `area: what changed`, the body says why.
  Commit locally; nothing is pushed or published without the person saying so.
- **Nothing is released yet: design forward.** No compatibility shims, no migrations for data that
  does not exist; change the format and its readers together.

## Security, which nothing may loosen

- **Ed25519 only.** SSH host and client keys (`sshd_config.d/10-construct.conf`), and release
  signing keys: `image check-key` (internal/update `ParseKeyring`) refuses a keyring with any other
  signing key, at build. No ECDSA, and RSA-2048 only where the firmware and the TPM take nothing
  else: the PCR key (`SIGNING_HOME/pcr.pem`, `task keygen:pcr`), because a TPM cannot verify
  Ed25519 and systemd verifies PCR policies as RSA. It signs kernel images' PCR 11 policies and
  nothing else (`image sign-pcr` refuses any other kind of key). Secure Boot's keys are RSA-2048
  for the same reason: UEFI verifies nothing else.
- **The release, PCR and Secure Boot keys never enter the repository or a container.** A
  release's are AWS KMS keys (infra `kms.tf`) that never leave KMS: the release builder asks KMS to
  sign. A person's own builds use files in `SIGNING_HOME`, outside the repo (`task
  keygen:release`, `keygen:pcr`, `keygen:secureboot`). Either way every key is a `crypto.Signer`
  named by a reference, `kms:<key>` or a PEM file (internal/sign), and `task usb` signs on the
  host: the PCR policy (ukify computes its digests in the container, `image sign-pcr` signs them,
  `image usb-join` joins the signature), Secure Boot's db over systemd-boot and the kernel image
  (`image usb-sign`), and `SHA256SUMS` (`image sign-release`, OpenPGP written in internal/sign over
  the signer, checked with go-crypto before it is written). The keys' public halves - the
  release's `release.pgp`, db's certificate and the signed Secure Boot variables - are in `keys/`
  for KMS (`task keys:kms`, once) and beside the files otherwise. Tests use throwaway keys in
  `.cache/test-keys`.
- **An installed machine's `/var` is LUKS2** (cmd/construct `makeVar`). The person's password opens
  it: as the TPM's PIN when the machine has a TPM, sealed to the signed policy of PCR 11, or as
  the passphrase when it has none. A recovery key is shown once at
  install. GDM then logs the person in, and sudo and the lock screen ask for the password again.
- **Updates come only through url-file transfers.** systemd-sysupdate checks `SHA256SUMS.gpg` and
  every file's digest only for `url-file` sources (via systemd-pull); a `regular-file` source is
  written unchecked. A stick is read as `file:///run/construct/update`, GitHub Releases as
  https. The transfers are generated in `cmd/image/transfers.go`; never hand-write one.
- **The test layer never reaches a real disk.** `image/test/` (unsafe-mode shell, the broken GDM)
  goes in only with `-test`/`-broken`; the harness's SSH key arrives as a per-boot credential
  (`ssh.ephemeral-authorized_keys-all`) and is written nowhere.
- **Nothing reaches the machine that it did not ask for.** The firewall (`usr/lib/construct/
  firewall.nft`, loaded by nftables.service before any network) drops what comes in but replies,
  what IPv4/IPv6, DHCP and mDNS need, the containers' bridges, and SSH and RDP, which answer only
  when a person turned them on; Docker publishes on 127.0.0.1 unless given an address. Open a port
  in the ruleset with its reason, never by widening a rule; `task test` checks that a network
  cannot reach a server listening on every address.
- **No person, password or private key in the image or the repository.** `construct install` asks
  who a machine is for and adds them to the installed `/etc`'s passwd and shadow, on `/var`
  (`internal/account`, starting from the image's own files in `usr/share/factory/etc`), where
  passwd and GNOME's Settings change them later; a root with nobody installed boots the
  passwordless `live` user `construct live` adds (construct-live.service). root stays locked (`*`).
  The person is logged in at boot once their password has opened `/var`; sudo (wheel, 15
  minutes) and the lock screen ask for it.

## What the root is, and what it implies

- The root is **read-only and verified block by block**. Whatever a package's install trigger
  would write at boot (GSettings schemas, icon/font/MIME caches, users, hwdb, ld.so.cache) is baked
  at build (`cmd/image/usb.go` `bake`); add to it when a new package needs one. The USB disk and
  the ext4 test disk are made from the same baked tree.
- **Units are enabled by preset**, never by a hand-made link: `usr/lib/systemd/system-preset/` and
  `user-preset/10-construct.preset` list what runs (everything else is off), and `bake` runs
  `systemctl preset-all`, which follows each unit's `[Install]` and makes its `Alias=` names.
- **Mutable state lives in `/var`**, a partition on the root's own disk: systemd-repart makes it
  in the initrd on a stick's first boot, `construct install` on an installed disk - its UUID derived
  from the machine id, as the Discoverable Partitions Specification asks, so the dissect_image udev
  builtin designates it only on its own machine and never on another disk. The initrd mounts it and
  lays `/etc` over it before the switch (image/initrd: systemd mounts /var itself only after). Home
  is `/var/home` (`/home`, `/opt`, `/srv`, `/mnt` and `/root` link into `/var`); NetworkManager's
  connections, SSH host keys, Flatpaks and Docker are on `/var`. A path a service writes in `/etc`
  or `/usr` works only through the overlay - check it on the USB disk, not only on the ext4 test
  disk.

## Recipes (`recipes/`, melange)

- Built with `task build -- <name>`, for **x86-64-v3** (`build/x86-64-v3.env`); a recipe that sets
  `CFLAGS` appends to `$CFLAGS`, never replaces it.
- **A changed recipe takes a new version or epoch**: the builder's cache knows a package by its
  apk's name, so the same name is the old build. `go test ./cmd/image` fails a recipe (or its
  directory) changed since origin/main that still makes the same apk.
- An **override of a Wolfi package** (mesa, pipewire, at-spi2-core, duktape) keeps Wolfi's package
  and subpackage names and takes an epoch above Wolfi's current one; read Wolfi's recipe first.
- melange's gaps, each found by booting: it derives **no dependency from an unversioned soname**
  (`libnss3.so`, `libSPIRV-Tools.so`) and **no provides for libraries in private directories**
  (`/usr/lib/mutter-51`) - declare both by hand. Wolfi ships some **pkg-config names it does not
  provide** (`xkeyboard-config` is `-2`, `cairomm-1.16`) - rewrite the `.pc` or write the
  dependencies out. `go run ./cmd/image check` fails any ELF whose libraries the root lacks.
- **Never rebuild a package while an image is being built from `packages/`**: rewriting a version
  in place under apko fails with a control hash mismatch.
- A Wolfi package that is stale, broken or built without what the desktop needs is a recipe here,
  with the reason in its comments - not a workaround in the image.

## The kernel and firmware

- The base is Arch Linux's configuration for the same kernel (pinned in `kernel/Dockerfile`):
  every driver a general distribution builds, as modules. `kernel/platform.config` (boot,
  systemd, hardening, what is never built) and then `kernel/desktop.config` (a PC's hardware) are
  merged over it, and **every line of both is asserted** after olddefconfig: a line is a promise.
  Fix a dropped symbol's dependency, do not delete the line. A PC is not a server in a controlled
  environment: do not close what a person's hardware or tools may need.
- Modules are signed by a key that exists only inside the build, with integrity lockdown: nothing
  built elsewhere loads.
- Compiled with Wolfi's Clang 23, ThinLTO, `-march=x86-64-v3`, AutoFDO and Propeller enabled for a profile from
  real hardware, full dynamic preemption, sched_ext (scx_lavd at boot). CachyOS's compiler side,
  none of its patches: out-of-tree scheduler patches are not taken.
- Firmware is the pinned linux-firmware, all of it but what no PC carries (ARM SoCs, data-centre
  network cards), zstd; microcode stays uncompressed (it rides in the UKI's early cpio).

## How a change is checked

| changed | run | and look at |
|---|---|---|
| Go | `go vet ./... && go test ./internal/... ./cmd/...` | a test for the behaviour, not for the lines |
| a recipe | `task build -- <name>`, `task rootfs`, `go run ./cmd/image disk -test && go run ./cmd/image check` | the package's files and `.PKGINFO` depends/provides |
| anything in the image or a package | `task test` (ext4 disk, kernel given directly) | the report, **and the screenshots in `_output/test`** - a PASS means a window opened, not that it looks right |
| boot, the initrd, `/var`, `/etc`, units, the read-only root | `task test:usb` (UEFI, a 32 GB USB stick, the verified root) | the same, on the disk a machine boots |
| install, updates, signatures, boot counting | `task test:lifecycle` | install onto NVMe, four forged sticks refused, update from a URL, rollback from a broken release, files in `/var` and `/etc` kept |
| the kernel | `task kernel:config`, then `task kernel` | the assertion's output |
| packages, their versions | `task usb`, then `task scan` (Grype, `build/grype.yaml`) | `_output/scan/report.md`, by severity: a Critical or High a version fixes fails it; what nobody has fixed is listed too |
| the boot's look (splash, Plymouth) | `go run ./cmd/image boot -usb -shots 30` | the frames in `_output/boot` |

To use it as a person does: `task play` (a window, the stick, a disk to install on; state in
`_output/play`). The graphical installer is UI only (Python, libadwaita): what it shows comes from
`construct disks --json` and `construct install --json`, and test:lifecycle installs through exactly its
command, pkexec included.

Tools for a boot that misbehaves: `go run ./cmd/image run [-usb | -vars -nvme -sticks] -- '<cmd>'`
runs a command as root in a booted VM; the harness saves failed units' logs in `units/`; `task usb
-- -append <option> [-append <option>...]` adds kernel options for one build; a disk's journal can be read
with `debugfs -c`. The initrd's units log to the journal; `rd.systemd.debug_shell` gets a shell there.

## Releases

- **A release is built on the release builder, never on a person's machine**: `task release`
  launches an EC2 instance for the pushed commit (infra repository, Construct-Linux/infra), which
  runs `task release:build` - the cache, the harness on a test build of the same commit
  (`test:usb`, `test:lifecycle`), the release signed with KMS, `task scan` - and terminates, at
  two hours at the latest. A check that fails stops the release.
- **A test build is never signed with a release's keys**: `image usb` records the test layer in
  `built.json` and `sign-pcr`/`usb-sign` refuse a KMS key for it.
- **Versions are CalVer**, `vYYYYMMDD.N` (`image release-version`); files carry them without the v.
- **Machines follow channels** (`construct channel`): a channel is a signed SHA256SUMS of its
  newest releases at the update CDN, each file a redirect to its GitHub Release asset. A release
  goes to `testing`; `task release:promote` offers it to `stable`; `task release:withdraw` takes a
  bad one out before machines take it.
- **Issues are where problems go**: `.github/labels.json` (`task labels`), the issue forms ask for
  `construct report`, and the weekly `watch` workflow files security fixes and big dependency
  changes, nothing routine (cmd/watch).

## Traps already fallen into

- **A scripted edit that matches nothing changes nothing, silently** (Python `str.replace`, `sed`
  on a gofmt-aligned line). Use edits that fail when they do not match, and grep for the result.
- **`set -e` does not stop a failing `a && b && c`**: only the list's last command counts, so a
  `find` busybox does not support skipped the firmware's copy and the build shipped 19 MB of it.
  One command per line under `set -e`, and assert what a step must have made.
- **Never pipe a gate's exit status away** (`task test | tail && ...`): redirect to a file and
  read the status.
- A VM stopped by QMP `quit` is a power cut: files written without fsync are empty afterwards.
  The lifecycle powers machines off; anything that writes a file a boot depends on syncs it.
- A oneshot that already ended is unloaded: look for it in the journal, not in `list-units`.
- `recipes/systemd` is Wolfi's systemd recipe with TPM2 and sysupdate turned on. Wolfi's own
  build has neither. Bump it whenever Wolfi bumps systemd, starting again from Wolfi's file.
- The kernel image on the ESP must stay writable (0644): systemd-boot counts a try by renaming it.

## Where to look next

AutoFDO/Propeller profiles recorded on the NucBox; boot time measured on the NucBox itself
(every report's `boot.txt` has the VM's: an installed disk 8.0 s, of it the initrd 1.6 s; the
stick 13.1 s, most of it GDM's first frame over emulated USB); the initrd's size (38 MB
compressed): the NSS and PAM modules it never uses, systemd-networkd and its kin, lvm2's
`lvm` that device-mapper's udev rules pull in with it; a services audit (`systemd-analyze
security` rates gdm, sshd and docker UNSAFE); fwupd's UEFI capsule updates, which need
fwupd-efi built and its binary signed with db's key; the lock libproxy's GIO resolver blocks geoclue on (its unit uses GLib's dummy
resolver meanwhile), to fix in `recipes/libproxy`.

## Pins

`build/versions.env` pins what every build fetches from outside: the Wolfi base image (whose
Clang 23 also builds the kernel), the kernel, linux-firmware, Intel's microcode and Grype. Taskfile.yml reads it (dotenv); a bump changes it and is checked like any other change.
