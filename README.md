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
