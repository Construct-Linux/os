# AGENTS.md - Construct

What an agent working here needs and the code cannot say: the rules, their reasons, and how a
change is checked. `README.md` says what the desktop is; `task --list` is the catalogue; git is
the history; issues hold what is open. Nothing here is a changelog, an incident log or a to-do
list: a rule earns its place by having a reason and a verification method, automated when
practical, and goes when its reason does.

Construct is Wolfi plus what Wolfi lacks for a GNOME desktop (`recipes/`), on its own kernel,
booted the way systemd's image-based distributions boot: a unified kernel image with a systemd
initrd, a read-only EROFS root under dm-verity found by its root hash, partitions of the
Discoverable Partitions Specification, A/B slots updated by systemd-sysupdate, and systemd-boot
falling back from a release that does not come up.

## How to work

- **Standards before our own code.** Before writing something, look for how systemd or the
  image-based distributions (ParticleOS, GNOME OS, SteamOS, Fedora's) solve it: prefer their
  generator, unit, repart definition or udev rule. Depart only when it delivers required product
  behaviour or demonstrably lowers complexity, cost or latency; record why beside the departure.
- **Performance is budgeted; stability is checked proportionally.** A change that can affect boot,
  session latency, CPU, memory, image size, battery or exposed services measures the relevant
  metric against a reproducible baseline. A regression needs a stated reason and an accepted
  budget. Run the narrowest test that proves a behaviour, escalating to the image, USB, lifecycle
  and release gates when their boundary changes. An unrelated change needs neither a performance
  number nor the full harness.
- **Design forward.** Nothing is released yet: do not add compatibility shims or migrations for
  data with no promised external or durable format; change such a format and its readers together.
  Once data crosses a documented external or persistent boundary, specify its compatibility,
  migration and rollback behaviour. A rule here is not a fence: when a better way appears, change
  the rule, its reason and its verification method in the same commit.
- **English, always**: code, comments, commit messages, docs, unit and task descriptions. The
  person you work for may write in another language; what lands in the repository is English.
- **Comments say why**, never what the line does or what it used to be: git keeps history.
- **Commits are partial and each one builds.** Subject `area: what changed`, the body says why.
  Commit locally; nothing is pushed or published without the person saying so.
- **Every build step is a Taskfile task.** A container run, a melange build, a disk, a test: done
  twice, it is a task, and the task is how it is done by people, agents and CI alike. Do not run
  `docker`, `melange` or `apko` by hand for what a task does; fix the task.
- **Durable decisions are Go, not shell.** `cmd/image` (disks, the initrd, the harness, releases),
  `cmd/construct` (install, boot-check, the live user, the theme), `internal/` (one package per
  concern). Short declarative or shell glue is appropriate when it is simpler, asserted and cannot
  grow into a second program. The Taskfile runs steps in short blocks that assert what they made;
  a unit's `ExecStart=` is a command, not a program.
- **Locales are the product's.** The desktop speaks every language its packages ship: build with
  NLS, split catalogs into `<name>-lang` (`split/locales`), install every `-lang` package in
  `image/apko.yaml` (`image check-langs` fails the build when one is missing), keep
  `glibc-locale-*`, and hard-code no language but the image's default
  (`image/rootfs/etc/locale.conf`).

## Security, which nothing may loosen

- **Approved cryptography only.** Today that is Ed25519 for SSH keys and release signing (`image
  check-key` refuses another signing key), and RSA-2048 where the firmware and TPM require it: the
  PCR key signs kernel images' PCR 11 policies and nothing else; Secure Boot's keys use what UEFI
  verifies. Change this profile only with a documented threat-model, compatibility and migration
  review.
- **The release, PCR and Secure Boot keys never enter the repository or a container.** A release
  signs with AWS KMS keys that never leave KMS; a person's own builds with files in
  `SIGNING_HOME`, outside the repo. Every key is a `crypto.Signer` named by a reference
  (internal/sign), and `task usb` signs on the host: Secure Boot's db over the root's EFI
  programs (systemd-boot, fwupd's capsule loader) before the root is sealed, the PCR policy, db
  over the kernel image, and `SHA256SUMS`. Public halves live in `keys/`.
- **A certificate is made once and never again**: firmware holds the certificate, not the key, and
  refuses a signature under a new certificate for the same key. A KEK or db that must hold
  something new is signed again under the same certificates (`task keys:kms --force`);
  `go test ./cmd/image` fails a certificate changed since origin/main. The KEK holds Microsoft's
  KEK CAs so that its dbx revocations apply; db holds its option ROM CAs and never the CAs for
  Windows or others' boot loaders (internal/sign/secureboot.go says why each).
- **A test build is never signed with a release's keys**: `image usb` records the test layer in
  `built.json`, and the signing steps refuse a KMS key for it. `image/test/` (unsafe-mode shell,
  the broken GDM) goes in only with `-test`/`-broken`; the harness's SSH key arrives as a per-boot
  credential and is written nowhere.
- **An installed machine's `/var` is LUKS2.** The person's password opens it: as the TPM's PIN,
  sealed to the signed policy of PCR 11, or as the passphrase without a TPM. A recovery key is
  shown once at install. GDM then logs the person in; sudo and the lock screen ask again.
- **Whatever else proves the person sits beside the password, never in its place.** A security key
  opens the disk with its PIN and a touch, and its touch is taken where sudo, polkit and the lock
  screen ask (`etc/pam.d/construct-key`: one root-written mappings file, a fixed origin, never
  `nouserok`). A finger is taken at the login and lock screens only (`etc/pam.d/gdm-fingerprint`);
  `task test` checks no other PAM service takes one.
- **Updates come only through url-file transfers**: systemd-sysupdate checks `SHA256SUMS.gpg` and
  every digest for `url-file` sources only; a `regular-file` source is written unchecked. A stick
  is read as `file:///run/construct/update`, the CDN as https. Transfers are generated
  (`cmd/image/transfers.go`), never hand-written.
- **Nothing reaches the machine that it did not ask for.** The firewall
  (`usr/lib/construct/firewall.nft`, before any network) drops what comes in but replies, what
  IPv4/IPv6, DHCP and mDNS need, the containers' bridges, and SSH and RDP when a person turned them
  on; Docker publishes on 127.0.0.1 unless given an address. Open a port with its reason, never by
  widening a rule; `task test` checks that a network cannot reach a server on every address.
- **No person, password or private key in the image or the repository.** `construct install` adds
  the person to the installed `/etc` on `/var` (internal/account); a root with nobody installed
  boots the passwordless `live` user. root stays locked.

## What the root is, and what it implies

- The root is **read-only and verified block by block**. Whatever a package's install trigger
  would write at boot (GSettings schemas, icon, font and MIME caches, users, hwdb, ld.so.cache) is
  baked at build (`cmd/image/usb.go` `bake`); add to it when a new package needs one. The USB disk
  and the ext4 test disk come from the same baked tree.
- **Units are enabled by preset**, never by a hand-made link: `usr/lib/systemd/system-preset/`
  and `user-preset/10-construct.preset` list what runs, everything else is off.
- **Mutable state lives in `/var`**, a partition on the root's own disk, its UUID derived from the
  machine id so that only its own machine designates it. The initrd mounts it and lays `/etc` over
  it before the switch. Home is `/var/home`; `/home`, `/opt`, `/srv`, `/mnt` and `/root` link into
  `/var`, as do NetworkManager's connections, SSH host keys, Flatpaks, Docker, and firmware the
  image cannot carry (`construct firmware`, searched before the root's). A path a service writes
  in `/etc` or `/usr` works only through the overlay: check it on the USB disk, not only on the
  ext4 test disk, and `image run -usb -poweroff` says whether a shutdown left the stick clean.

## Recipes (`recipes/`, melange)

- Built with `task build -- <name>` for **x86-64-v3** (`build/x86-64-v3.env`); a recipe that sets
  `CFLAGS` appends to `$CFLAGS`. `task rootfs` refuses a `packages/` that lacks what a recipe
  makes: apko would take Wolfi's package instead, and an override's whole point is what Wolfi's
  lacks. After a pull that bumps recipes, build each one it names first.
- **Every package is pinned in a lock, and a changed recipe goes through it.** `image/apko.lock.yaml`
  and `image/initrd/apko.lock.yaml` are what apko builds: the hand-written configuration plus
  every package at a version, recipes' first, Wolfi's after. **Only `task lock` writes them** (a
  bumped recipe at its build, Wolfi's as pinned, or Wolfi's newest with `-all`), and it builds both
  roots from what it wrote: a version taken from Wolfi's index without resolving can need what no
  other pin satisfies, and the builder found that out instead (nettle 4.0 beside a gnutls for
  nettle~3). The `lock` workflow runs `task lock:outdated`, which says what Wolfi has newer and
  changes nothing. `image check-lock` refuses a root that installs what the lock does not pin, or
  Wolfi's package where a recipe's should be: apko takes the newest version across repositories,
  so a Wolfi bump above an override silently replaces it and everything it carried. `task usb`
  refuses roots not built from the checkout's locks, and `test:usb` builds them as the release does.
- **A recipe whose produced package can change takes a new version or epoch**: the builder's cache
  knows a package by its apk's name. `go test ./cmd/image` fails a changed recipe that can make the
  same apk as origin/main; comments outside build blocks are not a change. An **override of a
  Wolfi package** keeps Wolfi's package and subpackage names, starts from
  Wolfi's recipe and takes an epoch above Wolfi's; Wolfi's index can carry a version whose recipe
  is not on GitHub yet, so start from the newest published recipe and upstream's tag.
- melange derives **no dependency from an unversioned soname** and **no provides for libraries in
  private directories**; declare both by hand. Wolfi ships some pkg-config names it does not
  provide; rewrite the `.pc` or write the dependencies out. `image check` fails any ELF whose
  libraries the root lacks.
- A Wolfi package that is stale, broken or built without what the desktop needs is a recipe here,
  with the reason in its comments, not a workaround in the image.
- **A fork follows its upstream through `task fork:sync`, `fork:check` and `fork:pin`** (cmd/fork):
  the merge, the tree's checks and the pin's move are a program's; a conflict, what of the fork's
  own fixes to keep and the recipe's comment are a person's or the fork-merge skill's
  (`.claude/skills/fork-merge`). The weekly `watch` files a fork behind its upstream; the weekly
  `forks` workflow merges the clean ones. A fork's branch is pushed by a person: the recipe
  clones it from GitHub.
- `build/versions.env` pins what every build fetches from outside (the Wolfi base image, the
  kernel, linux-firmware, microcode, Grype); a bump is checked like any other change.

## The kernel and firmware

- The base is Arch Linux's configuration for the same kernel (`kernel/Dockerfile`), with general-PC
  drivers as modules unless the supported-hardware policy and measured image or boot budgets say
  otherwise. Adopt an Arch kernel tag after its configuration assertions and relevant compatibility
  and performance checks pass; availability alone is not a release criterion.
  `kernel/platform.config` (boot, systemd, hardening) and `kernel/desktop.config` (a PC's hardware)
  are merged over it and **every line of both is asserted** after olddefconfig: fix a dropped
  symbol's dependency, do not delete the line. A PC is not a server in a controlled environment:
  do not close what a person's hardware or tools may need.
- Modules are signed by a key that exists only inside the build, with integrity lockdown.
- Compiled with Wolfi's Clang, ThinLTO, `-march=x86-64-v3`, AutoFDO and Propeller for a profile
  from real hardware, full dynamic preemption, sched_ext (scx_lavd at boot). CachyOS's compiler
  side, none of its patches.
- Firmware is the pinned linux-firmware, all of it but what no PC carries, zstd; microcode stays
  uncompressed in the UKI's early cpio.

## How a change is checked

Validation is tiered. Run the fast checks for the changed code or configuration first, then the
row for each affected boundary. A release runs every applicable gate. Do not run a slower unrelated
gate merely because it exists; do not omit a boundary gate because a faster one passed. Compare
performance reports only with a successful comparable baseline, and state the metric and budget in
the change when one applies.

| changed | run | and look at |
|---|---|---|
| Go | `task check` (what CI runs on a pull request) | a test for the behaviour, not for the lines |
| a recipe | `task build -- <name>`, `task lock` (which builds both roots), `task disk -- -test && go run ./cmd/image check` | the package's files, `.PKGINFO` depends/provides, and the lock's diff: the recipe's package and nothing of Wolfi's |
| an affected image or package behaviour | `task test` (ext4 disk, kernel given directly) | the report and relevant screenshots in `_output/test`: a PASS means a window opened, not that it looks right |
| a shell extension, or the shell itself | `task test` | `shell-startup.txt` (the session's shell from its unit to the last extension loaded, each extension's own load) and `idle.txt` (the session at rest: CPU, wakeups, what is busy), against the last build's |
| boot, the initrd, `/var`, `/etc`, units, the read-only root | `task test:usb` (UEFI, a USB stick, the verified root) | the same, on the disk a machine boots, and `boot.txt` against the last build's |
| install, updates, signatures, boot counting | `task test:lifecycle` | install onto NVMe, forged sticks refused, update from a URL, rollback from a broken release, `/var` and `/etc` kept |
| the kernel | `task kernel:config`, then `task kernel` | the assertion's output |
| packages, their versions | `task usb`, then `task scan` (Grype, `build/grype.yaml`) | `_output/scan/report.md`: a Critical or High a version fixes fails it; a rule that keeps one carries its reason |
| the boot's look (splash, Plymouth) | `go run ./cmd/image boot -usb -shots 30` | the frames in `_output/boot` |

Never pipe a gate's exit status away: redirect to a file and read the status.

To use it as a person does: `task play`. The graphical installer is UI only: what it shows comes
from `construct disks --json` and `construct install --json`, and test:lifecycle installs through
exactly its command.

For a boot that misbehaves: `go run ./cmd/image run [-usb | -vars -nvme -sticks] -- '<cmd>'` runs
a command as root in a booted VM; the harness saves failed units' logs in `units/`; `task usb --
-append <option>` adds kernel options for one build; `rd.systemd.debug_shell` gets a shell in the
initrd, whose units log to the journal.

## Releases

- **A release is built on the release builder, never on a person's machine**: `task release`
  launches an EC2 instance for the pushed commit (Construct-Linux/infra), which runs the harness
  on a test build of the same commit, signs with KMS, scans, and terminates. A check that fails
  stops the release. `task release` refuses a commit `task test:usb` has not passed on this
  machine: the builder finds the same failure two hours and a machine later.
- **Versions are CalVer**, `vYYYYMMDD.N`; files carry them without the v.
- **Machines follow channels**: a channel is a signed SHA256SUMS of its newest releases at the
  update CDN. A release goes to `testing`; promote it only after its stated soak criterion and
  required lifecycle evidence pass, and record that evidence with the promotion. Then
  `task release:promote` offers it to `stable`; `task release:withdraw` takes a bad one out before
  machines take it. The criterion may be elapsed time, successful boots, a cohort, or a combination
  appropriate to the release's risk.
- **Issues are where problems go**, and where open work goes: the issue forms ask for
  `construct report`, the weekly `watch` workflow files security fixes, and pins and forks behind upstream,
  Dependabot opens the Go modules' and the actions' bumps.
