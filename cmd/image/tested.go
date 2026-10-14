package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// tested is what the harness's last run on this machine says: which commit it ran on, whether
// the checkout was that commit and nothing more, which disk booted, whether every scenario ran
// and whether it passed. image test writes it beside the report (_output/test/tested.json);
// task release asks image tested before it launches a builder, so a commit no one ran the stick's
// harness on is not found out on the builder, two hours and a machine later.
type tested struct {
	Commit string    `json:"commit"`
	Clean  bool      `json:"clean"`
	USB    bool      `json:"usb"`
	Full   bool      `json:"full"`
	Passed bool      `json:"passed"`
	Time   time.Time `json:"time"`
}

const testedFile = "tested.json"

// recordTested writes dir's tested.json for a run of the harness: on the stick when usb, of
// every scenario when full, and passed when err is nil.
func recordTested(dir string, usb, full bool, err error) {
	rec := tested{USB: usb, Full: full, Passed: err == nil, Time: time.Now().UTC()}
	if head, dirty, gitErr := checkout(); gitErr == nil {
		rec.Commit, rec.Clean = head, !dirty
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, testedFile), append(b, '\n'), 0o644)
}

// checkout is HEAD, and whether a tracked file differs from it.
func checkout() (head string, dirty bool, err error) {
	h, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	d, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return "", false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(h)), len(d) > 0, nil
}

// testedCheck (image tested [-usb]) is nil when the harness's last run was of HEAD, as committed,
// every scenario, on the stick when -usb, and passed; otherwise it says what is missing.
func testedCheck(args []string) error {
	fl := flag.NewFlagSet("tested", flag.ExitOnError)
	usb := fl.Bool("usb", false, "the run must have booted the USB disk (task test:usb)")
	_ = fl.Parse(args)
	b, err := os.ReadFile(filepath.Join(out, "test", testedFile))
	if err != nil {
		return fmt.Errorf("no run of the harness on this machine: %w", err)
	}
	var rec tested
	if err := json.Unmarshal(b, &rec); err != nil {
		return fmt.Errorf("%s: %w", testedFile, err)
	}
	head, _, err := checkout()
	if err != nil {
		return err
	}
	return rec.covers(head, *usb)
}

// covers is nil when the run stands for head: on the stick when usb.
func (t tested) covers(head string, usb bool) error {
	task := "task test"
	if usb {
		task = "task test:usb"
	}
	switch {
	case t.Commit != head:
		return fmt.Errorf("the harness last ran on %.12s, not on HEAD %.12s: %s first", t.Commit, head, task)
	case !t.Clean:
		return fmt.Errorf("the harness ran on %.12s with changes not committed to it: commit them and run %s again", t.Commit, task)
	case usb && !t.USB:
		return errors.New("the harness last ran on the ext4 test disk, not on the stick: task test:usb first")
	case !t.Full:
		return fmt.Errorf("the harness last ran some scenarios (-only), not all: %s first", task)
	case !t.Passed:
		return fmt.Errorf("the harness failed on %.12s (%s/test/report.txt): fix it, then %s again", t.Commit, out, task)
	}
	return nil
}
