# Construct

A GNOME desktop on [Wolfi](https://github.com/wolfi-dev), built and verified like Spin OS
(spin-ami): one image, read-only and checked block by block, updated as a whole into a second
slot, and rolled back by itself when a new version does not come up.

- **GNOME 51** on Wayland, with GNOME's core apps (Files, Text Editor, Calendar, Maps, Software,
  Papers, ...), Ptyxis, Firefox, and Flatpak with Flathub (Chrome).
- **Its own look**: Orchis in Construct's colors (dark) for the shell, GTK 3 and libadwaita apps alike, following
  the light/dark switch; Tela icons; Bibata's cursor. A top bar with the clock and indicators on
  the right, and a small centred dock (Dash to Panel). Extensions: AppIndicator, Clipboard
  Indicator, Just Perfection, Caffeine, Vitals, Tiling Shell.
- **Built for GNOME 51, fixed where it is not**: the theme, the icons, the cursor and six of the
  extensions come from forks with a `gnome-51` branch - upstream's issues triaged against this
  stack, one commit per fix - and each recipe pins its commit and names the fixes it takes.
- **What Wolfi lacks is built here**: about 110 melange recipes in `recipes/` - the GNOME stack,
  and Wolfi packages rebuilt where Wolfi's fall short (mesa with Intel's drivers and Vulkan,
  PipeWire with Bluetooth audio, systemd-sysupdate, ...).
- **Its own kernel**: spin-ami's, with modules signed by a key that exists only in its build, built
  with Clang ThinLTO for x86-64-v3, sched_ext with scx_lavd.
- **Boot**: UEFI, systemd-boot, a unified kernel image with Plymouth playing the brand's animation and a
  systemd initrd, the way systemd's image-based distributions boot. `/var` holds everything a
  machine keeps; `/etc` is an overlay on it, so settings persist.
- **Updates**: signed (Ed25519) releases from GitHub Releases, checked daily, written into the slot
  not running; or from a release's USB stick. Nothing reboots by itself; a version that does not
  bring the desktop up three times is dropped for the one before it.

Developer tools ship with it: Docker and containerd, gcc and make, git.

## Use it

Everything is a task (`task --list`); the build needs Docker, Go, QEMU (and swtpm) and
`../spin-ami`.

```sh
task keygen:release                      # once: the release key, outside this repository
task usb                                  # -> _output/usb/disk.raw
```

Write `_output/usb/disk.raw` to a USB stick and boot it. The stick is three things:

- **live**: the desktop runs from it, as the user `live`; its first boot makes `/var` in the rest
  of the stick.
- **installer**: the live session opens *Install Construct* (or `sudo construct install` in a
  terminal), which asks for the disk and who the machine is for - a name, a login and one
  password - then erases the machine's first internal disk (never the stick) and lays two system
  slots and `/var` on it. `/var` holds everything of the person's and is encrypted (LUKS2): at
  boot the password opens it - as the TPM's PIN where the machine has a TPM 2.0, which only
  releases the key to kernel images Construct signed - and the desktop starts logged in. sudo and
  the lock screen ask for it again. The installer shows a recovery key once, which opens the disk
  in place of a forgotten password. root has no password and cannot log in.
- **update**: plugged into an installed machine, it writes its release into the slot not running.

Secure Boot: systemd-boot and every kernel image are signed with Construct's key, which the
stick carries for the firmware. Put the firmware's Secure Boot in setup mode (its menu calls it
"clear keys", "reset to setup mode" or "delete all keys"), boot the stick and choose *Enroll
Secure Boot keys: auto*; the machine restarts and from then on boots only what Construct signed.
The keys keep Microsoft's certificates for graphics and network cards' own firmware, not
Windows'.

An installed machine goes back to how it left the installer with `sudo systemctl start
factory-reset.target`: it restarts, `/var` is made anew (every person, file, container and
setting gone; the system slots untouched), and GNOME's Initial Setup asks who it is for. The
disk still opens with the password it was installed with: to give the machine to someone else,
install it again from the stick.

## Test it

`task test` drives the desktop in QEMU (every app, Settings, the shell's own views and the lock
screen, each extension's state, audio, Docker, failed units, core dumps, the shell's log, with a
screenshot of each); `task test:usb` does the same booted from a USB stick by UEFI;
`task play` boots the stick in a window on your desktop with an empty disk to install it on -
the way to try the installer and live with the result (`task play:stick` builds that stick).
`task test:lifecycle` installs onto an NVMe disk, refuses forged updates, updates from a URL and
rolls back from a broken release. `AGENTS.md` says what each change needs.

## Layout

| path | what |
|---|---|
| `recipes/` | melange recipes, built into `packages/` (`task build -- <name>`) |
| `image/` | the root: `apko.yaml`, and `rootfs/` laid over it; `test/` is the harness's only |
| `kernel/`, `firmware/` | the desktop's kernel fragment and firmware selection, over spin-ami's |
| `cmd/image` | disks, the USB stick, releases, the QEMU harness and lifecycle |
| `image/initrd/` | the initrd: Wolfi's systemd, `/var` and the `/etc` overlay mounted before the switch |
| `cmd/construct` | `install`, `disks` and `boot-check` on the machine |
| `image/rootfs/usr/bin/construct-installer` | the graphical installer: a libadwaita front-end to `construct install --json` |
| `internal/` | GPT, and installing a signed release |
| `cmd/gap` | how far Wolfi is from a desktop profile, in recipes to own |
