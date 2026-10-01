# spin-desktop

Experiment: can Spin OS (Wolfi, spin-ami's machinery) replace Ubuntu on a desktop with a UI?

Target machine: GMKtec NucBox K10 (i9-13900HK), Intel Iris Xe (i915), AX201 Wi-Fi + Bluetooth,
RTL8125, HDA audio. Today: Ubuntu 26.04, GNOME on Wayland, Firefox and VS Code as snaps.

## How far Wolfi is from a desktop

`go run ./cmd/gap` takes each profile's runtime dependency closure from Alpine edge and checks
it against Wolfi's index. A source package Wolfi lacks is a melange recipe we would own and
rebuild on every upstream release. Measured on 2026-09-30:

| profile  | source packages | in Wolfi | recipes to own |
|----------|----------------:|---------:|---------------:|
| plumbing |             187 |      156 |             30 |
| sway     |             243 |      182 |             60 |
| niri     |             317 |      212 |            103 |
| gnome    |             392 |      243 |            146 |
| plasma   |             457 |      249 |            203 |

"Plumbing" is what every profile carries: Mesa, PipeWire, NetworkManager, BlueZ, Flatpak,
fwupd, Xwayland, power. Wolfi does maintain the low layers (mesa 26.2, gtk-4 4.24, pipewire,
systemd 261, libinput, firefox 157); it has no desktop session, no NetworkManager, no
WirePlumber, no Flatpak and no `linux-firmware`.

projectbluefin/wolfi-gnome tried GNOME on Wolfi: 70 recipes, one commit (2025-09), session not
reached; Bluefin then built Dakota on GNOME OS instead.

## Beyond packages

- Firmware: Wolfi ships none. This machine loads `i915/adlp_dmc.bin`, `i915/adlp_guc_70.bin`,
  `i915/tgl_huc.bin`, `iwlwifi-so-a0-hr-b0-89.ucode`, `intel/ibt-0040-4150.{sfi,ddc}`; pinned
  from linux-firmware. spin-ami's kernel has no modules and no initrd, so built-in drivers
  need their blobs in `CONFIG_EXTRA_FIRMWARE`.
- Kernel: metal.config gains i915, iwlwifi/mac80211, btusb, HDA/SOF, r8169, input, suspend.
- Updates: metal reinstalls from the medium; a desktop needs A/B (systemd-sysupdate) and a
  rebuild whenever Firefox or Mesa ship a security fix.
- Apps: snaps go away; Firefox from Wolfi or Flathub, VS Code from Flathub or a tarball in /var.

## E1 + E2: GNOME 51 on Wolfi, in QEMU (2026-10-01)

- `recipes/`: 72 melange recipes (70 from the first session's graph, plus blueprint-compiler,
  colord-gtk and a duktape that links libm), built in dependency order by a workflow with one
  agent per recipe. All of GNOME's core is 51.0: mutter, gnome-shell, gdm, gnome-session,
  gnome-settings-daemon, gnome-control-center. Off on purpose: webkit (no captive-portal
  helper), ModemManager (patched out of control-center), flatpak/ostree, malcontent, cups-pk-helper,
  sysprof, nautilus.
- `kernel/`: spin-ami's host.config + metal.config + `desktop.config` - modules, signed by a
  per-build key, with integrity lockdown; 197 modules, 23 MB. `firmware/`: spin-ami's pinned
  linux-firmware, desktop families only, zstd (310 MB).
- `task rootfs disk boot`: apko root, ext4 without root privileges, QEMU with virtio-gpu.
  `go run ./cmd/image check` fails any ELF whose libraries the root lacks.

GDM logs a test user in and GNOME Shell runs as the Wayland compositor (overview, top bar,
network and sound). What the boots found, each now fixed in a recipe or the image:

- Wolfi's duktape 2.7.0-r2 has no NEEDED libm: polkitd and every GIO process that loads
  libproxy's PAC backend die in the loader. Ours links it (epoch 3).
- melange generates no dependency from unversioned sonames (libnss3.so) nor provides for
  libraries in private directories (/usr/lib/mutter-51); both declared by hand.
- apko runs no triggers: schemas, font, icon and MIME caches are written by a unit at boot.
- Typelibs: GLib's are in glib-gir, cairo's only in gobject-introspection-dev.
- GDM 51's greeters are dynamic users: nsswitch needs `systemd`.

Still open: no rtkit (Wolfi has none), at-spi's bus unit, bolt, wallpapers, screencast, Vulkan
(Wolfi's mesa is gallium only), and E3 - the NucBox from a USB stick.
