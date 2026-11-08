package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/needle"
)

// wifiAPs is how many networks the wifi scenario puts in the air: more than the shell lists
// (network.js shows 8 and folds the rest into "More networks"), so a list that grows with the
// networks around shows here, as it did in a busy building where the menu went off the screen.
const wifiAPs = 12

// wifiSetup gives the machine Wi-Fi: mac80211_hwsim's radios, one the machine's own and the rest
// access points. NetworkManager is told to leave the access points alone, so the shell and
// Settings see one adapter and the networks it scans; wpa_supplicant's AP mode (CONFIG_AP, in its
// recipe) runs each network, WPA2 with a throwaway passphrase, each a unit of its own: a daemon
// the command started went with its SSH session, and the networks with it.
const wifiSetup = `set -e
modprobe mac80211_hwsim radios=%[1]d
for i in $(seq 1 50); do [ -e /sys/class/net/wlan%[2]d ] && break; sleep 0.2; done
mkdir -p /run/harness-wifi
for i in $(seq 1 %[2]d); do
  until nmcli device set wlan$i managed no 2>/dev/null; do sleep 0.2; done
  cat > /run/harness-wifi/$i.conf <<EOF
network={
  ssid="Harness network $i"
  mode=2
  frequency=$((2412 + 5 * ((i - 1) %% 11)))
  key_mgmt=WPA-PSK
  proto=RSN
  pairwise=CCMP
  psk="harness-network-$i"
}
EOF
  systemd-run --unit=harness-ap$i --quiet wpa_supplicant -s -i wlan$i -c /run/harness-wifi/$i.conf
done
`

// lfWifiMenu opens quick settings and the Wi-Fi toggle's menu and says where the menu is: the
// menu that left the screen is the defect this scenario is for.
const lfWifiMenu = `
	const m = Main.panel.statusArea.quickSettings.menu;
	m.open();
	await new Promise(r => GLib.timeout_add(GLib.PRIORITY_DEFAULT, 800, () => r()));
	const t = lfAll(m.box, lfClass('quick-toggle-has-menu')).find(t => t.is_mapped() && t.constructor.name.includes('Wireless'));
	t?.menu.open(true);
	await new Promise(r => GLib.timeout_add(GLib.PRIORITY_DEFAULT, 3000, () => r()));
	const e = m.box.get_transformed_extents();
	const y = e.origin.y, h = e.size.height;
	const items = t ? lfAll(t.menu.box, a => a.constructor.name.includes('NetworkItem') && a.is_mapped()).length : -1;
	return {toggle: !!t, open: m.isOpen, mapped: m.actor.is_mapped(), y, h, stage: global.stage.height, items,
		submenu: t?.menu.isOpen ?? false};`

type wifiMenuSeen struct {
	Toggle, Open, Mapped, Submenu bool
	Y, H, Stage                   float64
	Items                         int
}

// wifi is the scenario: the networks up, the machine scanning them, the shell's menu of them and
// Settings' Wi-Fi panel.
func (r *report) wifi(g *guest, l *lf) {
	out, err := g.run(fmt.Sprintf(wifiSetup, wifiAPs+1, wifiAPs), "")
	r.add("wifi access points", err == nil, fmt.Sprintf("%d networks by mac80211_hwsim%s%s", wifiAPs, lastLines(out, 4), errText(err)))
	if err != nil {
		return
	}
	var seenN int
	ok := until(60*time.Second, 3*time.Second, func() bool {
		s, _ := g.run(`nmcli -t -f SSID device wifi list ifname wlan0 --rescan yes`, "")
		seenN = strings.Count(s, "Harness network")
		return seenN >= wifiAPs
	})
	r.add("wifi scan", ok, fmt.Sprintf("wlan0 sees %d of %d networks", seenN, wifiAPs))
	var s wifiMenuSeen
	err = g.evalJSON(lfWifiMenu, &s)
	time.Sleep(time.Second)
	l.shot(viewMeta{"shell-quick-settings-wifi", needle.GroupShell,
		"Quick settings with the Wi-Fi menu open among a dozen networks: the menu whole on the screen, the networks listed in the well",
		[]string{"S2", "E3"}}, nil)
	r.add("wifi menu", err == nil && s.Toggle && s.Open && s.Mapped && s.Submenu && s.Items > 0 && s.Y >= 0 && s.Y+s.H <= s.Stage,
		fmt.Sprintf("%+v%s", s, errText(err)))
	_, _ = g.eval(lfPrelude + `const m = Main.panel.statusArea.quickSettings.menu; lfAll(m.box, lfClass('quick-toggle-has-menu')).forEach(t => t.menu?.close(false)); m.close(); true`)
	time.Sleep(time.Second)
	ok, detail := g.runPanel("wifi")
	r.add("panel:wifi networks", ok, detail)
	l.shot(viewMeta{"panel-wifi-networks", needle.GroupSettings,
		"Settings' Wi-Fi panel with a dozen networks in range: the list's rows, their signal and lock",
		[]string{"S3", "TY4"}}, nil)
	g.quitApp(settingsApp)
}
