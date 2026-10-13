package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Construct-Linux/os/internal/power"
)

// sleepMessageID marks the journal entry the sleep hook writes for each suspend, which construct
// power suspend reads back.
const sleepMessageID = "8b1d2f6c3a4e4d0f9c7b5a2e1f6d3c4b"

// sleepState is where the hook keeps the machine as it was before a suspend, for after it.
const sleepState = "/run/construct/power-sleep.json"

// powerCmd says what the machine draws, measured over a few seconds, or with "suspend" what each
// suspend since the journal began cost.
func powerCmd(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "suspend" {
		return powerSuspends(stdout)
	}
	fl := flag.NewFlagSet("power", flag.ContinueOnError)
	seconds := fl.Int("seconds", 10, "how long to measure over")
	if err := fl.Parse(args); err != nil {
		return err
	}
	a, b, err := power.Sysfs{Root: "/"}.Measure(ctx, time.Duration(*seconds)*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, describePower(a, b))
	return nil
}

// describePower is the person-readable account of what the machine drew between a and b.
func describePower(a, b power.Snapshot) string {
	var out bytes.Buffer
	switch {
	case b.Batteries == 0:
		fmt.Fprintln(&out, "No battery: on mains power.")
	case !b.OnBattery:
		fmt.Fprintf(&out, "On mains power, battery at %.0f%% (%.1f of %.1f Wh).\n", 100*b.EnergyWh/b.FullWh, b.EnergyWh, b.FullWh)
	default:
		// The batteries' own reading moves in steps; what they lost over the interval is steadier
		// once it moved at all.
		w := power.BatteryW(a, b)
		if w == 0 {
			w = b.DrawW
		}
		left := ""
		if w > 0 {
			left = fmt.Sprintf(", %s left", (time.Duration(b.EnergyWh / w * float64(time.Hour))).Round(time.Minute))
		}
		fmt.Fprintf(&out, "On battery at %.0f%% (%.1f of %.1f Wh), drawing %.1f W%s.\n", 100*b.EnergyWh/b.FullWh, b.EnergyWh, b.FullWh, w, left)
	}
	switch w, ok := power.PackageW(a, b); {
	case ok:
		fmt.Fprintf(&out, "CPU package: %.1f W.\n", w)
	case b.NoPackage:
		fmt.Fprintln(&out, "CPU package: this machine has no RAPL counter to read.")
	default:
		fmt.Fprintln(&out, "CPU package: unreadable (RAPL's counter is root's: sudo construct power).")
	}
	return out.String()
}

// sleepHook is systemd-sleep's hook (usr/lib/systemd/system-sleep/construct-power, a link to
// construct): before a suspend it keeps the machine's state in /run, after it it writes what the
// suspend cost to the journal.
func sleepHook(args []string) error {
	if len(args) < 2 || args[1] != "suspend" {
		return nil
	}
	snap, err := power.Sysfs{Root: "/"}.Snapshot()
	if err != nil {
		return err
	}
	switch args[0] {
	case "pre":
		b, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(sleepState), 0o755); err != nil {
			return err
		}
		return os.WriteFile(sleepState, b, 0o644)
	case "post":
		b, err := os.ReadFile(sleepState)
		if err != nil {
			return err
		}
		var before power.Snapshot
		if err := json.Unmarshal(b, &before); err != nil {
			return err
		}
		_ = os.Remove(sleepState)
		s := power.Between(before, snap)
		return journalSend(map[string]string{
			"MESSAGE":                   sleepSummary(s),
			"MESSAGE_ID":                sleepMessageID,
			"PRIORITY":                  "6",
			"SYSLOG_IDENTIFIER":         "construct-power",
			"CONSTRUCT_SLEPT_USEC":      strconv.FormatInt(s.Slept.Microseconds(), 10),
			"CONSTRUCT_HW_SLEEP_USEC":   strconv.FormatInt(s.HWSleep.Microseconds(), 10),
			"CONSTRUCT_USED_MWH":        strconv.FormatFloat(s.UsedWh*1000, 'f', 0, 64),
			"CONSTRUCT_FULL_MWH":        strconv.FormatFloat(s.FullWh*1000, 'f', 0, 64),
			"CONSTRUCT_ON_BATTERY":      strconv.FormatBool(before.OnBattery && snap.OnBattery),
			"CONSTRUCT_SUSPEND_SUCCESS": strconv.Itoa(snap.Suspends),
		})
	}
	return nil
}

// sleepSummary is one suspend in a sentence.
func sleepSummary(s power.Sleep) string {
	// Minutes for a night, seconds for a short sleep, which minutes would round to nothing.
	slept := s.Slept.Round(time.Second)
	if s.Slept >= time.Hour {
		slept = s.Slept.Round(time.Minute)
	}
	msg := fmt.Sprintf("Slept %s, the hardware in its low-power state %.0f%% of it", slept, s.LowPowerPercent())
	if s.FullWh > 0 {
		msg += fmt.Sprintf("; used %.2f Wh, %.2f%% of the battery an hour", s.UsedWh, s.PercentPerHour())
	}
	return msg + "."
}

// powerSuspends lists what each suspend the journal recorded cost.
func powerSuspends(stdout io.Writer) error {
	out, err := exec.Command("journalctl", "--no-pager", "-o", "json", "MESSAGE_ID="+sleepMessageID).Output()
	if err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		us, _ := strconv.ParseInt(fmt.Sprint(e["__REALTIME_TIMESTAMP"]), 10, 64)
		fmt.Fprintf(stdout, "%s  %s\n", time.UnixMicro(us).Format("2006-01-02 15:04"), e["MESSAGE"])
		n++
	}
	if n == 0 {
		fmt.Fprintln(stdout, "No suspend recorded yet.")
	}
	return sc.Err()
}

// journalSend writes one entry to the journal by its native protocol, its fields kept as fields.
// No value here holds a newline, which the protocol would need another encoding for.
func journalSend(fields map[string]string) error {
	var b bytes.Buffer
	for k, v := range fields {
		if bytes.ContainsRune([]byte(v), '\n') {
			return errors.New("journal field " + k + " holds a newline")
		}
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	c, err := net.Dial("unixgram", "/run/systemd/journal/socket")
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(b.Bytes())
	return err
}
