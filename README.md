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
  from linux-firmware, read from /usr/lib/firmware when each driver's module loads.
- Kernel: metal's, with modules - i915, xe, amdgpu, Wi-Fi, Bluetooth, HDA/SOF, docks - each
  signed by a key that exists only inside its build.
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

## Usable, and tested as such (2026-10-01)

`task test` boots a test disk in QEMU (gnome-shell in unsafe mode, sshd over vsock with a key
from an SMBIOS credential, an HDA card) and drives the session through org.gnome.Shell.Eval:
28 checks - every app opens a window (Terminal, Settings, Files, Text Editor, Calculator, Image
Viewer, System Monitor, Disks, Firefox, Chrome from Flathub), nine Settings panels open,
WirePlumber has a sink, sched_ext runs scx_lavd, Docker runs a container as the user, and no
unit failed and nothing dumped core. Screenshots of every step go to `_output/test`.

What it took beyond the first session, ~110 recipes in all:

- Overrides of Wolfi's own packages: **mesa** (Wolfi's has no Intel driver at all - no iris,
  crocus or anv - and no Vulkan), **pipewire** (no Bluetooth audio, no GStreamer plugin),
  **at-spi2-core** (no systemd units), **duktape** (no libm).
- Wolfi's packaging gaps found by booting, now each a line in a recipe or the image: unversioned
  sonames (NSS, SPIRV-Tools) and libraries in private directories get no dependency or provides
  from melange; pkg-config names Wolfi ships but does not provide (xkeyboard-config,
  cairomm-1.16); firefox without a desktop entry; dockerd and containerd without units; avahi
  without its user; iptables pointing at legacy xtables.
- CachyOS's compiler side, without its patches: kernel ThinLTO, AutoFDO and Propeller builds
  ready for a perf profile from the NucBox, x86-64-v3 for the kernel and every recipe, full
  dynamic preemption, sched_ext with scx_lavd instead of BORE.

Still open: the AutoFDO/Propeller profile (needs the real machine), Firefox is unbranded
("Nightly"), Bluetooth audio has SBC and Opus only (Wolfi has no LDAC/aptX/AAC/LC3), and E3 -
a UKI/verity disk on the NucBox from a USB stick.
