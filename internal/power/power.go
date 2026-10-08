// Package power reads what the kernel says about a machine's energy: its batteries' charge and
// draw, the CPU package's energy counter (RAPL), and how long the hardware spent in its low-power
// state while suspended. A laptop's power is measured from these, not guessed.
package power

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Snapshot is the machine at one moment.
type Snapshot struct {
	Time time.Time
	// OnBattery is whether no mains supply is online.
	OnBattery bool
	// Batteries is how many system batteries there are (not a mouse's).
	Batteries int
	// EnergyWh and FullWh are what the batteries hold now and when full.
	EnergyWh, FullWh float64
	// DrawW is what the batteries report they deliver now, 0 when charging or unknown.
	DrawW float64
	// PackageJ is the CPU package's energy counter, in joules; HavePackage is whether it could be
	// read (RAPL's counter is root's alone).
	PackageJ, PackageMaxJ float64
	HavePackage           bool
	// HWSleep is the time the hardware has spent in its lowest power state across every suspend
	// since boot; Suspends how many suspends succeeded.
	HWSleep  time.Duration
	Suspends int
}

// Sysfs reads a snapshot from the kernel's files under Root ("/" on a machine).
type Sysfs struct {
	Root string
}

func (s Sysfs) path(rel string) string { return filepath.Join(s.Root, rel) }

// Snapshot reads the power supplies, the CPU package's energy and the suspend counters now.
func (s Sysfs) Snapshot() (Snapshot, error) {
	snap := Snapshot{Time: time.Now(), OnBattery: true}
	supplies, err := os.ReadDir(s.path("sys/class/power_supply"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return snap, err
	}
	for _, e := range supplies {
		dir := s.path(filepath.Join("sys/class/power_supply", e.Name()))
		switch read(dir, "type") {
		case "Mains", "USB":
			if read(dir, "online") == "1" {
				snap.OnBattery = false
			}
		case "Battery":
			// A wireless mouse's or a keyboard's battery is a power supply too: scope Device.
			if read(dir, "scope") == "Device" {
				continue
			}
			snap.Batteries++
			now, full := micro(dir, "energy_now"), micro(dir, "energy_full")
			volts := micro(dir, "voltage_now")
			if now == 0 && full == 0 {
				// Batteries that count charge (µAh) instead of energy.
				now, full = micro(dir, "charge_now")*volts, micro(dir, "charge_full")*volts
			}
			snap.EnergyWh += now
			snap.FullWh += full
			if read(dir, "status") == "Discharging" {
				w := micro(dir, "power_now")
				if w == 0 {
					w = micro(dir, "current_now") * volts
				}
				snap.DrawW += w
			}
		}
	}
	if snap.Batteries == 0 {
		snap.OnBattery = false
	}
	rapl := s.path("sys/class/powercap/intel-rapl:0")
	if v, err := os.ReadFile(filepath.Join(rapl, "energy_uj")); err == nil {
		if uj, err := strconv.ParseFloat(strings.TrimSpace(string(v)), 64); err == nil {
			snap.PackageJ, snap.HavePackage = uj/1e6, true
			snap.PackageMaxJ = micro(rapl, "max_energy_range_uj")
		}
	}
	us, _ := strconv.ParseInt(read(s.path("sys/power/suspend_stats"), "total_hw_sleep"), 10, 64)
	snap.HWSleep = time.Duration(us) * time.Microsecond
	snap.Suspends, _ = strconv.Atoi(read(s.path("sys/power/suspend_stats"), "success"))
	return snap, nil
}

// Measure snapshots the machine, waits d, and snapshots it again: what it drew in between. A ctx
// that ends first ends the wait, with its error.
func (s Sysfs) Measure(ctx context.Context, d time.Duration) (Snapshot, Snapshot, error) {
	a, err := s.Snapshot()
	if err != nil {
		return a, a, err
	}
	select {
	case <-ctx.Done():
		return a, a, ctx.Err()
	case <-time.After(d):
	}
	b, err := s.Snapshot()
	return a, b, err
}

// Battery is one of the system's batteries as the kernel reports it.
type Battery struct {
	Name, Status string
	// Capacity is its charge, in percent.
	Capacity int
	// Health is what it holds when full against what it was designed to, in percent; 0 when the
	// kernel does not say.
	Health float64
	// Cycles is how many times it was charged, 0 when unknown.
	Cycles int
}

// Batteries are the system's batteries - not a mouse's or a keyboard's - in the kernel's order.
func (s Sysfs) Batteries() []Battery {
	supplies, _ := os.ReadDir(s.path("sys/class/power_supply"))
	var out []Battery
	for _, e := range supplies {
		dir := s.path(filepath.Join("sys/class/power_supply", e.Name()))
		if read(dir, "type") != "Battery" || read(dir, "scope") == "Device" {
			continue
		}
		b := Battery{Name: e.Name(), Status: read(dir, "status")}
		b.Capacity, _ = strconv.Atoi(read(dir, "capacity"))
		b.Cycles, _ = strconv.Atoi(read(dir, "cycle_count"))
		full, design := micro(dir, "energy_full"), micro(dir, "energy_full_design")
		if full == 0 {
			// Batteries that count charge instead of energy.
			full, design = micro(dir, "charge_full"), micro(dir, "charge_full_design")
		}
		if design > 0 {
			b.Health = 100 * full / design
		}
		out = append(out, b)
	}
	return out
}

// PackageW is the CPU package's mean power between two snapshots, false when unreadable. The
// counter wraps at PackageMaxJ.
func PackageW(a, b Snapshot) (float64, bool) {
	dt := b.Time.Sub(a.Time).Seconds()
	if !a.HavePackage || !b.HavePackage || dt <= 0 {
		return 0, false
	}
	j := b.PackageJ - a.PackageJ
	if j < 0 {
		j += b.PackageMaxJ
	}
	return j / dt, true
}

// BatteryW is the batteries' mean draw between two snapshots, from what they held: steadier than
// one reading of their draw. Zero when they did not lose energy.
func BatteryW(a, b Snapshot) float64 {
	h := b.Time.Sub(a.Time).Hours()
	if h <= 0 || b.EnergyWh >= a.EnergyWh {
		return 0
	}
	return (a.EnergyWh - b.EnergyWh) / h
}

// Sleep is one suspend, measured from the snapshots before and after it.
type Sleep struct {
	// Slept is the wall time asleep, HWSleep the part of it the hardware spent in its low-power
	// state.
	Slept, HWSleep time.Duration
	// UsedWh is the energy the batteries lost, FullWh what they hold when full.
	UsedWh, FullWh float64
}

// Between is the suspend between a, taken before it, and b, taken after.
func Between(a, b Snapshot) Sleep {
	s := Sleep{Slept: b.Time.Sub(a.Time), HWSleep: b.HWSleep - a.HWSleep, FullWh: b.FullWh}
	if a.EnergyWh > b.EnergyWh {
		s.UsedWh = a.EnergyWh - b.EnergyWh
	}
	return s
}

// PercentPerHour is the share of a full battery lost per hour asleep.
func (s Sleep) PercentPerHour() float64 {
	if s.FullWh <= 0 || s.Slept <= 0 {
		return 0
	}
	return 100 * s.UsedWh / s.FullWh / s.Slept.Hours()
}

// LowPowerPercent is the share of the time asleep the hardware spent in its low-power state: a
// laptop that does not reach it drains as if half awake.
func (s Sleep) LowPowerPercent() float64 {
	if s.Slept <= 0 {
		return 0
	}
	return 100 * s.HWSleep.Seconds() / s.Slept.Seconds()
}

func read(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// micro reads a value the kernel gives in millionths (µWh, µW, µV, µJ) as units.
func micro(dir, name string) float64 {
	v, err := strconv.ParseFloat(read(dir, name), 64)
	if err != nil {
		return 0
	}
	return v / 1e6
}
