package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// kernelProfileCmd records the kernel at work on this machine for its AutoFDO profile, as the
// kernel's Documentation/dev-tools/autofdo.rst asks: every CPU's taken branches in kernel code, by
// the processor's last-branch records, sampled every 500009 branches, while the machine is used
// as it is every day. task kernel:profile turns the recording into kernel/profiles/autofdo.afdo.
func kernelProfileCmd(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("kernel-profile", flag.ContinueOnError)
	minutes := fl.Int("minutes", 10, "how long to record")
	out := fl.String("o", "/var/tmp/kernel.perf", "the recording")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("recording every CPU's branches is root's: sudo construct kernel-profile")
	}
	cpuinfo, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return err
	}
	// AMD's event (RETIRED_TAKEN_BRANCH_INSTRUCTIONS) is named through libpfm4, which this perf is
	// built without.
	if !strings.Contains(string(cpuinfo), "GenuineIntel") {
		return errors.New("only Intel processors are recorded yet: AMD's branch event needs perf with libpfm4")
	}
	fmt.Fprintf(stdout, "Recording the kernel for %d minutes into %s. Use the machine as every day meanwhile.\n", *minutes, *out)
	rec := exec.Command("perf", "record", "-e", "BR_INST_RETIRED.NEAR_TAKEN:k", "-a", "-N", "-b",
		"-c", "500009", "-o", *out, "--", "sleep", strconv.Itoa(*minutes*60))
	rec.Stdout, rec.Stderr = stdout, os.Stderr
	if err := rec.Run(); err != nil {
		return fmt.Errorf("perf record: %w", err)
	}
	fmt.Fprintf(stdout, "Done. On the machine that built this release, from the same commit:\n  task kernel:profile PERF=%s\n", *out)
	return nil
}
