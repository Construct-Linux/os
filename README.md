# Spin Desktop

A GNOME desktop on [Wolfi](https://github.com/wolfi-dev), built and verified like Spin OS
(spin-ami): one image, read-only and checked block by block, updated as a whole into a second
slot, and rolled back by itself when a new version does not come up.

- **GNOME 51** on Wayland, with GNOME's core apps (Files, Text Editor, Calendar, Maps, Software,
  Papers, ...), Ptyxis, Firefox, and Flatpak with Flathub (Chrome).
- **Its own look**: Orchis (teal, dark) for the shell, GTK 3 and libadwaita apps alike, following
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
- **Boot**: UEFI, systemd-boot, a unified kernel image with SpinBot's splash and Plymouth, no
  initrd. `/var` holds everything a machine keeps; `/etc` is an overlay on it, so settings persist.
- **Updates**: signed (Ed25519) releases from GitHub Releases, checked daily, written into the slot
  not running; or from a release's USB stick. Nothing reboots by itself; a version that does not
  bring the desktop up three times is dropped for the one before it.

Developer tools ship with it: Docker and containerd, gcc and make, git.

## Use it

Everything is a task (`task --list`); the build needs Docker, Go, QEMU and `../spin-ami`.

```sh
task keygen:release                      # once: the release key, outside this repository
openssl passwd -6 > .cache/spin-password.hash   # the user's password
task usb                                  # -> _output/usb/disk.raw
```

Write `_output/usb/disk.raw` to a USB stick and boot it. The stick is three things:

- **live**: the desktop runs from it; its first boot makes `/var` in the rest of the stick.
- **installer**: `sudo spin-desktop install` erases the machine's first internal disk (never the
  stick) and lays two system slots and `/var` on it.
- **update**: plugged into an installed machine, it writes its release into the slot not running.

## Test it

`task test` drives the desktop in QEMU (every app, Settings, the shell's own views and the lock
screen, each extension's state, audio, Docker, failed units, core dumps, the shell's log, with a
screenshot of each); `task test:usb` does the same booted from a USB stick by UEFI;
`task test:lifecycle` installs onto an NVMe disk, refuses forged updates, updates from a URL and
rolls back from a broken release. `AGENTS.md` says what each change needs.

## Layout

| path | what |
|---|---|
| `recipes/` | melange recipes, built into `packages/` (`task build -- <name>`) |
| `image/` | the root: `apko.yaml`, and `rootfs/` laid over it; `test/` is the harness's only |
| `kernel/`, `firmware/` | the desktop's kernel fragment and firmware selection, over spin-ami's |
| `cmd/image` | disks, the USB stick, releases, the QEMU harness and lifecycle |
| `cmd/spin-init` | the first process: `/var`, and `/etc` over it, before systemd |
| `cmd/spin-desktop` | `install` and `boot-check` on the machine |
| `internal/` | GPT, and installing a signed release |
| `cmd/gap` | how far Wolfi is from a desktop profile, in recipes to own |
