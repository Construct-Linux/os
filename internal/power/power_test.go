package power

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSys(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// A laptop on battery: an energy-counting battery and a charge-counting one add up, a mouse's
// battery does not count, and the hardware's sleep and RAPL's counter are read.
func TestSnapshot(t *testing.T) {
	root := t.TempDir()
	writeSys(t, root, map[string]string{
		"sys/class/power_supply/AC/type":                      "Mains",
		"sys/class/power_supply/AC/online":                    "0",
		"sys/class/power_supply/BAT0/type":                    "Battery",
		"sys/class/power_supply/BAT0/status":                  "Discharging",
		"sys/class/power_supply/BAT0/energy_now":              "30000000",
		"sys/class/power_supply/BAT0/energy_full":             "60000000",
		"sys/class/power_supply/BAT0/power_now":               "7500000",
		"sys/class/power_supply/BAT1/type":                    "Battery",
		"sys/class/power_supply/BAT1/status":                  "Discharging",
		"sys/class/power_supply/BAT1/voltage_now":             "12000000",
		"sys/class/power_supply/BAT1/charge_now":              "1000000",
		"sys/class/power_supply/BAT1/charge_full":             "2000000",
		"sys/class/power_supply/BAT1/current_now":             "250000",
		"sys/class/power_supply/mouse/type":                   "Battery",
		"sys/class/power_supply/mouse/scope":                  "Device",
		"sys/class/power_supply/mouse/energy_now":             "5000000",
		"sys/class/powercap/intel-rapl:0/energy_uj":           "2500000",
		"sys/class/powercap/intel-rapl:0/max_energy_range_uj": "262143328850",
		"sys/power/suspend_stats/total_hw_sleep":              "3600000000",
		"sys/power/suspend_stats/success":                     "2",
	})
	s, err := Sysfs{Root: root}.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !s.OnBattery || s.Batteries != 2 {
		t.Errorf("on battery %v, batteries %d", s.OnBattery, s.Batteries)
	}
	if !near(s.EnergyWh, 42) || !near(s.FullWh, 84) || !near(s.DrawW, 10.5) {
		t.Errorf("energy %v of %v Wh, draw %v W", s.EnergyWh, s.FullWh, s.DrawW)
	}
	if s.Package != RAPLRead || !near(s.PackageJ, 2.5) {
		t.Errorf("package %v J (%v)", s.PackageJ, s.Package)
	}
	if s.HWSleep != time.Hour || s.Suspends != 2 {
		t.Errorf("hw sleep %v, suspends %d", s.HWSleep, s.Suspends)
	}
}

// A desktop without a battery is on mains, whatever its supplies say.
func TestNoBattery(t *testing.T) {
	s, err := Sysfs{Root: t.TempDir()}.Snapshot()
	if err != nil || s.OnBattery || s.Package != RAPLAbsent {
		t.Errorf("%+v, %v", s, err)
	}
}

// A night asleep: 8 h, 2.4 Wh of a 60 Wh battery is 0.5 %/h; 6 h of it in the low-power state.
func TestSleep(t *testing.T) {
	t0 := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	a := Snapshot{Time: t0, EnergyWh: 40, FullWh: 60, HWSleep: time.Hour}
	b := Snapshot{Time: t0.Add(8 * time.Hour), EnergyWh: 37.6, FullWh: 60, HWSleep: 7 * time.Hour}
	s := Between(a, b)
	if !near(s.PercentPerHour(), 0.5) || !near(s.LowPowerPercent(), 75) {
		t.Errorf("%%/h %v, low power %v%%", s.PercentPerHour(), s.LowPowerPercent())
	}
	if w := BatteryW(a, b); !near(w, 0.3) {
		t.Errorf("battery %v W", w)
	}
}

// RAPL's counter wraps at its range: the power across a wrap is still the energy spent.
func TestPackageWraps(t *testing.T) {
	t0 := time.Now()
	a := Snapshot{Time: t0, Package: RAPLRead, PackageJ: 99, PackageMaxJ: 100}
	b := Snapshot{Time: t0.Add(2 * time.Second), Package: RAPLRead, PackageJ: 5, PackageMaxJ: 100}
	if w, ok := PackageW(a, b); !ok || !near(w, 3) {
		t.Errorf("%v W (%v)", w, ok)
	}
}

// A battery's health is its full charge against its design's, in energy or, where it reports
// none, in charge; a mouse's battery is not the system's.
func TestBatteries(t *testing.T) {
	root := t.TempDir()
	writeSys(t, root, map[string]string{
		"sys/class/power_supply/BAT0/type":               "Battery",
		"sys/class/power_supply/BAT0/status":             "Discharging",
		"sys/class/power_supply/BAT0/capacity":           "81",
		"sys/class/power_supply/BAT0/energy_full":        "45000000",
		"sys/class/power_supply/BAT0/energy_full_design": "50000000",
		"sys/class/power_supply/BAT0/cycle_count":        "212",
		"sys/class/power_supply/BAT1/type":               "Battery",
		"sys/class/power_supply/BAT1/status":             "Full",
		"sys/class/power_supply/BAT1/capacity":           "100",
		"sys/class/power_supply/BAT1/charge_full":        "3000000",
		"sys/class/power_supply/BAT1/charge_full_design": "4000000",
		"sys/class/power_supply/hid-mouse/type":          "Battery",
		"sys/class/power_supply/hid-mouse/scope":         "Device",
	})
	got := Sysfs{Root: root}.Batteries()
	want := []Battery{
		{Name: "BAT0", Status: "Discharging", Capacity: 81, Health: 90, Cycles: 212},
		{Name: "BAT1", Status: "Full", Capacity: 100, Health: 75},
	}
	if len(got) != len(want) {
		t.Fatalf("Batteries = %+v", got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Status != want[i].Status || got[i].Capacity != want[i].Capacity ||
			!near(got[i].Health, want[i].Health) || got[i].Cycles != want[i].Cycles {
			t.Errorf("battery %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A measurement whose context ends stops waiting, and says why.
func TestMeasureCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, _, err := (Sysfs{Root: t.TempDir()}).Measure(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("Measure = %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("Measure waited out its minute")
	}
}
