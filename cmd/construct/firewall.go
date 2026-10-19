package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/firewall"
	"github.com/Construct-Linux/os/internal/program"
)

// firewallCmd is construct firewall (internal/firewall): status, log, allow, deny, unblock, and
// watch, which construct-firewall-notice.service runs in the session to tell the person of a scan.
func firewallCmd(ctx context.Context, args []string, out io.Writer) error {
	usage := errors.New("usage: construct firewall status | log | allow PORT/tcp|udp [from ADDRESS|NETWORK] | deny NAME | unblock ADDRESS | watch")
	if len(args) == 0 {
		return usage
	}
	switch args[0] {
	case "status":
		return firewallStatus(ctx, out)
	case "log":
		events, err := firewall.Log(ctx)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			fmt.Fprintln(out, "nothing logged this boot: no unsolicited packet reached this machine, or none since it booted.")
		}
		for _, e := range events {
			fmt.Fprintln(out, eventLine(e))
		}
		return nil
	case "allow":
		if len(args) != 2 && (len(args) != 4 || args[2] != "from") {
			return usage
		}
		port, proto, ok := strings.Cut(args[1], "/")
		p, err := strconv.Atoi(port)
		if !ok || err != nil {
			return fmt.Errorf("%q: PORT/tcp or PORT/udp", args[1])
		}
		from := ""
		if len(args) == 4 {
			from = args[3]
		}
		r, err := firewall.NewRule(p, proto, from)
		if err != nil {
			return err
		}
		if err := firewall.Allow(ctx, r); err != nil {
			return err
		}
		fmt.Fprintf(out, "allowed %s, as %s (%s/%s.nft): construct firewall deny %s takes it back.\n", r, r.Name, firewall.RulesDir, r.Name, r.Name)
		return nil
	case "deny":
		if len(args) != 2 {
			return usage
		}
		if err := firewall.Deny(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "denied again: %s\n", args[1])
		return nil
	case "unblock":
		if len(args) != 2 {
			return usage
		}
		addr, err := netip.ParseAddr(args[1])
		if err != nil {
			return fmt.Errorf("%q is no address", args[1])
		}
		if err := firewall.Unblock(ctx, addr); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s may try again.\n", addr)
		return nil
	case "watch":
		return firewallWatch(ctx, out)
	}
	return usage
}

func firewallStatus(ctx context.Context, out io.Writer) error {
	counters, err := firewall.Counters(ctx)
	if err != nil {
		return err
	}
	blocked, err := firewall.Scanners(ctx)
	if err != nil {
		return err
	}
	events, err := firewall.Log(ctx)
	if err != nil {
		return err
	}
	rules, err := firewall.Rules("/")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, firewall.Summary(counters, events, blocked))
	fmt.Fprintf(out, "ssh: %d packets accepted (it answers only while a person turned it on)\n", counters["ssh"].Packets)
	if len(rules) == 0 {
		fmt.Fprintln(out, "your rules: none (construct firewall allow PORT/tcp [from NETWORK])")
	} else {
		fmt.Fprintln(out, "your rules:")
		for _, r := range rules {
			fmt.Fprintf(out, "  %-40s %-28s %d packets\n", r.Name, r, counters[r.Name].Packets)
		}
	}
	for _, b := range blocked {
		fmt.Fprintf(out, "holding %s as a scanner for %s more (construct firewall unblock %s)\n", b.Addr, b.Expires.Round(time.Second), b.Addr)
	}
	return nil
}

func eventLine(e firewall.Event) string {
	kind := "dropped"
	if e.Scan {
		kind = "scan from"
	}
	when := ""
	if !e.Time.IsZero() {
		when = e.Time.Format("2006-01-02 15:04:05 ")
	}
	return fmt.Sprintf("%s%s %s %s/%d", when, kind, e.Source, e.Proto, e.Port)
}

// firewallWatch follows the kernel's journal for scans and tells the person of each source once
// in a while, through the session's notifications: what the firewall holds, where from.
func firewallWatch(ctx context.Context, out io.Writer) error {
	c := exec.CommandContext(ctx, "journalctl", "-k", "-f", "-n", "0", "-q", "-o", "short-iso", "-g", firewall.Prefix+"scan: ")
	r, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	told := map[netip.Addr]time.Time{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		for _, e := range firewall.ParseLog(strings.NewReader(sc.Text() + "\n")) {
			if !e.Scan || !e.Source.IsValid() || time.Since(told[e.Source]) < 10*time.Minute {
				continue
			}
			told[e.Source] = time.Now()
			fmt.Fprintln(out, eventLine(e))
			_ = program.Run(ctx, "notify-send", "--app-name=Construct", "--icon=security-high", "--urgency=normal",
				"Someone is scanning this computer",
				fmt.Sprintf("%s tried port after port; the firewall holds it for ten minutes. construct firewall log says more.", e.Source))
		}
	}
	return c.Wait()
}
