# AGENTS.md - Spin Desktop

What an agent working here needs and the code cannot say: rules, their reasons, and how a change
is checked. `README.md` says what the desktop is; `task --list` is the catalogue; git is the
history. Nothing here is a changelog.

Spin Desktop is Wolfi plus what Wolfi lacks for a GNOME desktop (`recipes/`), on spin-ami's
kernel and boot design: a unified kernel image, a read-only EROFS root under dm-verity, no
initrd, A/B slots updated by systemd-sysupdate, and systemd-boot falling back from a release
that does not come up.

## The rules

- **English, always**: code, comments, commit messages, docs, unit descriptions, task
  descriptions. The person you work for may write in another language; what lands in the
  repository is English.
- **Locales are the product's, not the repository's**: the desktop speaks every language its
  packages ship. Never strip translations to save space: keep `-lang`/locale subpackages buildable,
  keep `glibc-locale-*` in the image, and do not hard-code a language anywhere but the image's
  default (`image/rootfs/etc/locale.conf`).
- **Every build step is a Taskfile task.** A container run, a melange build, a disk, a test: if it
  is done twice it is a task, and the task is how it is done - by people, agents and CI alike.
  `task build -- <recipe>` builds a recipe, `task rootfs`, `task kernel`, `task firmware`,
  `task usb`, `task test`, `task test:usb`, `task test:lifecycle`. Do not run `docker`/`melange`
  /`apko` by hand for something a task does; add or fix the task instead.
- **Decisions are Go, not shell.** `cmd/image` (disks, the harness, releases), `cmd/spin-init`
  (PID 1 before systemd), `cmd/spin-desktop` (install, boot-check), `internal/` (gpt, update).
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

- **Ed25519 only.** SSH host and client keys (`sshd_config.d/10-spin-desktop.conf`), and release
  signing keys: `image check-key` (internal/update `ParseKeyring`) refuses a keyring with any other
  signing key, at build. No RSA, no ECDSA.
- **The release key never enters the repository or a container.** It lives in `SIGNING_HOME`
  (`task keygen:release`, outside the repo); `task usb` signs `SHA256SUMS` on the host. Tests use a
  throwaway key in `.cache/test-gnupg`.
- **Updates come only through url-file transfers.** systemd-sysupdate checks `SHA256SUMS.gpg` and
  every file's digest only for `url-file` sources (via systemd-pull); a `regular-file` source is
  written unchecked. A stick is read as `file:///run/spin-desktop/update`, GitHub Releases as
  https. The transfers are generated in `cmd/image/transfers.go`; never hand-write one.
- **The test layer never reaches a real disk.** `image/test/` (unsafe-mode shell, the broken GDM)
  goes in only with `-test`/`-broken`; the harness's SSH key arrives as a per-boot credential
  (`ssh.ephemeral-authorized_keys-all`) and is written nowhere.
- **No password or private key in the repository.** The user's password hash is read from
  `.cache/spin-password.hash` at build.

## What the root is, and what it implies

- The root is **read-only and verified block by block**. Whatever a package's install trigger
  would write at boot (GSettings schemas, icon/font/MIME caches, users, hwdb, ld.so.cache) is baked
  at build (`cmd/image/usb.go` `bake`); add to it when a new package needs one.
- **Mutable state lives in `/var`**, a partition on the root's own disk: `cmd/spin-init` makes it
  on a stick's first boot (moving the GPT's backup to the disk's end first) and mounts it before
  systemd, then lays `/etc` as an overlay on it. Home is `/var/home`; NetworkManager's connections,
  SSH host keys, Flatpaks and Docker are on `/var`. A path a service writes in `/etc` or `/usr`
  works only through the overlay - check it on the USB disk, not only on the ext4 test disk.
- **Never mount `/run` for systemd** (spin-init unmounts the one systemd-repart needs): a `/run`
  systemd finds mounted breaks its services' journal sockets.
- `/var` and partitions are found by **reading the GPT** of the root's disk, never by label across
  all disks: a live stick and an installed disk both have a `var`.

## Recipes (`recipes/`, melange)

- Built with `task build -- <name>`, for **x86-64-v3** (`build/x86-64-v3.env`); a recipe that sets
  `CFLAGS` appends to `$CFLAGS`, never replaces it.
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

- `kernel/desktop.config` is merged over spin-ami's `host.config` and `metal.config` (read from
  `../spin-ami` as a build context, never copied) and **every line is asserted** after
  olddefconfig: a line is a promise. Fix a dropped symbol's dependency, do not delete the line.
- Modules are signed by a key that exists only inside the build, with integrity lockdown: nothing
  built elsewhere loads.
- Compiled with Clang ThinLTO, `-march=x86-64-v3`, AutoFDO and Propeller enabled for a profile from
  real hardware, full dynamic preemption, sched_ext (scx_lavd at boot). CachyOS's compiler side,
  none of its patches: out-of-tree scheduler patches are not taken.
- Firmware is spin-ami's pinned linux-firmware, desktop families only, zstd; microcode stays
  uncompressed (it rides in the UKI's early cpio).

## How a change is checked

| changed | run | and look at |
|---|---|---|
| Go | `go vet ./... && go test ./internal/... ./cmd/...` | a test for the behaviour, not for the lines |
| a recipe | `task build -- <name>`, `task rootfs`, `go run ./cmd/image disk -test && go run ./cmd/image check` | the package's files and `.PKGINFO` depends/provides |
| anything in the image or a package | `task test` (ext4 disk, kernel given directly) | the report, **and the screenshots in `_output/test`** - a PASS means a window opened, not that it looks right |
| boot, `/var`, `/etc`, spin-init, units, the read-only root | `task test:usb` (UEFI, a 32 GB USB stick, the verified root) | the same, on the disk a machine boots |
| install, updates, signatures, boot counting | `task test:lifecycle` | install onto NVMe, four forged sticks refused, update from a URL, rollback from a broken release, files in `/var` and `/etc` kept |
| the kernel | `task kernel:config`, then `task kernel` | the assertion's output |
| the boot's look (splash, Plymouth) | `go run ./cmd/image boot -usb -shots 30` | the frames in `_output/boot` |

Tools for a boot that misbehaves: `go run ./cmd/image run [-usb | -vars -nvme -sticks] -- '<cmd>'`
runs a command as root in a booted VM; the harness saves failed units' logs in `units/`; `task usb
-- -append <cmdline>` adds kernel options (one token) for one build; a disk's journal can be read
with `debugfs -c`. spin-init's errors go to the kernel log at a level a quiet boot shows.

## Traps already fallen into

- **A scripted edit that matches nothing changes nothing, silently** (Python `str.replace`, `sed`
  on a gofmt-aligned line). Use edits that fail when they do not match, and grep for the result.
- **Never pipe a gate's exit status away** (`task test | tail && ...`): redirect to a file and
  read the status.
- A VM stopped by QMP `quit` is a power cut: files written without fsync are empty afterwards.
  The lifecycle powers machines off; anything that writes a file a boot depends on syncs it.
- A oneshot that already ended is unloaded: look for it in the journal, not in `list-units`.
- Wolfi's systemd has no sysupdate; `recipes/systemd-sysupdate` builds its standalone binary from
  Wolfi's exact systemd tag (a meson patch adds the target). Bump it whenever Wolfi bumps systemd.
- The kernel image on the ESP must stay writable (0644): systemd-boot counts a try by renaming it.

## Where to look next

AutoFDO/Propeller profiles recorded on the NucBox; boot time (spin-init, units on the critical
path); geoclue's start timeout on the USB disk; a services audit (what runs that need not, which
units can be sandboxed further); Secure Boot and measured boot, as spin-ami does; an ISO medium.

## Two repositories

spin-ami (`../spin-ami`) is the server and bare-metal image whose kernel fragments, pins and boot
design this one builds on; its `versions.yaml` pins the kernel, the toolchain, linux-firmware and
the Wolfi base image used here. A change to what both share is made there.
