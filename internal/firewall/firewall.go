// Package firewall reads and changes the machine's firewall (usr/lib/construct/firewall.nft, an
// nftables ruleset nftables.service loads): what its rules counted, the sources it holds as
// scanners, what it logged to the kernel's journal, and the rules a person adds in
// /etc/nftables.d, one file each, written and removed here and never by hand. Every decision is
// here; cmd/construct firewall is the command, and construct report reads a summary.
package firewall

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Construct-Linux/os/internal/fsx"
	"github.com/Construct-Linux/os/internal/program"
)

const (
	// Ruleset is the firewall, which includes RulesDir's files in its input chain.
	Ruleset = "/usr/lib/construct/firewall.nft"
	// RulesDir holds the person's rules, one file each, named by Rule.Name.
	RulesDir = "/etc/nftables.d"
	// Prefix starts every line the firewall logs, followed by "scan: " or "drop: ".
	Prefix = "construct-fw "
	table  = "inet construct"
)

// A Counter is a rule's count of what it matched, by the comment the rule carries.
type Counter struct {
	Packets, Bytes uint64
}

// Counters reads every commented rule's counter from nft's JSON.
func Counters(ctx context.Context) (map[string]Counter, error) {
	out, err := nft(ctx, "-j", "list", "table", "inet", "construct")
	if err != nil {
		return nil, err
	}
	return ParseCounters(out)
}

// ParseCounters reads `nft -j list table` output: the counter of each rule with a comment.
func ParseCounters(b []byte) (map[string]Counter, error) {
	var t struct {
		Nftables []struct {
			Rule *struct {
				Comment string            `json:"comment"`
				Expr    []json.RawMessage `json:"expr"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("nft's table: %w", err)
	}
	counters := map[string]Counter{}
	for _, o := range t.Nftables {
		if o.Rule == nil || o.Rule.Comment == "" {
			continue
		}
		for _, e := range o.Rule.Expr {
			var c struct {
				Counter *Counter `json:"counter"`
			}
			if json.Unmarshal(e, &c) == nil && c.Counter != nil {
				counters[o.Rule.Comment] = *c.Counter
			}
		}
	}
	return counters, nil
}

// A Blocked source: held as a scanner, for Expires more.
type Blocked struct {
	Addr    netip.Addr
	Expires time.Duration
}

// Scanners are the sources the firewall holds as scanners now, IPv4's and IPv6's.
func Scanners(ctx context.Context) ([]Blocked, error) {
	var all []Blocked
	for _, set := range []string{"scanners", "scanners6"} {
		out, err := nft(ctx, "-j", "list", "set", "inet", "construct", set)
		if err != nil {
			return nil, err
		}
		b, err := ParseScanners(out)
		if err != nil {
			return nil, err
		}
		all = append(all, b...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Expires > all[j].Expires })
	return all, nil
}

// ParseScanners reads `nft -j list set` output: each element and how long it has left.
func ParseScanners(b []byte) ([]Blocked, error) {
	var t struct {
		Nftables []struct {
			Set *struct {
				Elem []json.RawMessage `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("nft's set: %w", err)
	}
	var blocked []Blocked
	for _, o := range t.Nftables {
		if o.Set == nil {
			continue
		}
		for _, raw := range o.Set.Elem {
			// An element with a timeout is {"elem": {"val": addr, "expires": seconds}}; one
			// without is the address alone.
			var e struct {
				Elem struct {
					Val     string  `json:"val"`
					Expires float64 `json:"expires"`
				} `json:"elem"`
			}
			var val string
			if json.Unmarshal(raw, &e) == nil && e.Elem.Val != "" {
				val = e.Elem.Val
			} else if json.Unmarshal(raw, &val) != nil {
				continue
			}
			addr, err := netip.ParseAddr(val)
			if err != nil {
				continue
			}
			blocked = append(blocked, Blocked{addr, time.Duration(e.Elem.Expires * float64(time.Second))})
		}
	}
	return blocked, nil
}

// Unblock lets a source go before its time is up.
func Unblock(ctx context.Context, addr netip.Addr) error {
	set := "scanners"
	if addr.Is6() {
		set = "scanners6"
	}
	_, err := nft(ctx, "delete", "element", "inet", "construct", set, "{ "+addr.String()+" }")
	return err
}

// An Event is a line the firewall logged: a scan found, or an unsolicited packet dropped.
type Event struct {
	Time   time.Time
	Scan   bool
	Source netip.Addr
	Proto  string
	Port   int
}

var (
	logLine = regexp.MustCompile(`construct-fw (scan|drop): (.*)$`)
	field   = regexp.MustCompile(`\b(SRC|PROTO|DPT)=(\S+)`)
)

// ParseLog reads journal lines (journalctl -o short-iso) for the firewall's.
func ParseLog(r io.Reader) []Event {
	var events []Event
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		m := logLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		e := Event{Scan: m[1] == "scan"}
		if t, err := time.Parse("2006-01-02T15:04:05-0700", strings.Fields(line)[0]); err == nil {
			e.Time = t
		}
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			switch f[1] {
			case "SRC":
				e.Source, _ = netip.ParseAddr(f[2])
			case "PROTO":
				e.Proto = strings.ToLower(f[2])
			case "DPT":
				e.Port, _ = strconv.Atoi(f[2])
			}
		}
		events = append(events, e)
	}
	return events
}

// Log is what the firewall logged this boot, oldest first.
func Log(ctx context.Context) ([]Event, error) {
	var out bytes.Buffer
	err := (program.With{Stdout: &out}).Run(ctx, "journalctl", "-k", "-b", "-q", "--no-pager", "-o", "short-iso", "-g", Prefix)
	if err != nil {
		return nil, err
	}
	return ParseLog(&out), nil
}

// A Rule a person added: what it lets in. Name is its file's and its counter's.
type Rule struct {
	Name  string
	Port  int
	Proto string
	From  netip.Prefix // From.IsValid() false: anyone
}

// NewRule is the rule for port/proto, from everyone or from a prefix, named after them.
func NewRule(port int, proto string, from string) (Rule, error) {
	if port < 1 || port > 65535 {
		return Rule{}, fmt.Errorf("port %d: 1 to 65535", port)
	}
	if proto != "tcp" && proto != "udp" {
		return Rule{}, fmt.Errorf("%q: tcp or udp", proto)
	}
	r := Rule{Port: port, Proto: proto, Name: fmt.Sprintf("allow-%d-%s", port, proto)}
	if from != "" {
		p, err := netip.ParsePrefix(from)
		if err != nil {
			a, aerr := netip.ParseAddr(from)
			if aerr != nil {
				return Rule{}, fmt.Errorf("%q: an address or a network, 192.168.1.0/24", from)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		r.From = p.Masked()
		r.Name += "-from-" + strings.NewReplacer(":", "_", "/", "_").Replace(r.From.String())
	}
	return r, nil
}

// Line is the rule as nft reads it: a rule of the input chain, counted under its name.
func (r Rule) Line() string {
	var b strings.Builder
	if r.From.IsValid() {
		family := "ip"
		if r.From.Addr().Is6() {
			family = "ip6"
		}
		fmt.Fprintf(&b, "%s saddr %s ", family, r.From)
	}
	fmt.Fprintf(&b, "%s dport %d counter accept comment %q", r.Proto, r.Port, r.Name)
	return b.String()
}

// String says what the rule lets in, for a person.
func (r Rule) String() string {
	s := fmt.Sprintf("%d/%s", r.Port, r.Proto)
	if r.From.IsValid() {
		return s + " from " + r.From.String()
	}
	return s + " from anyone"
}

var ruleLine = regexp.MustCompile(`^(?:(?:ip|ip6) saddr (\S+) )?(tcp|udp) dport (\d+) counter accept comment "([^"]+)"$`)

// ParseRule reads a rule file's line back.
func ParseRule(line string) (Rule, bool) {
	m := ruleLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return Rule{}, false
	}
	port, _ := strconv.Atoi(m[3])
	r := Rule{Name: m[4], Port: port, Proto: m[2]}
	if m[1] != "" {
		r.From, _ = netip.ParsePrefix(m[1])
	}
	return r, true
}

// Rules are the person's, from RulesDir under root, by name.
func Rules(root string) ([]Rule, error) {
	files, _ := filepath.Glob(filepath.Join(root, RulesDir, "*.nft"))
	var rules []Rule
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if r, ok := ParseRule(line); ok {
				rules = append(rules, r)
			}
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules, nil
}

// Allow writes the rule's file and loads the ruleset with it, checked first: a ruleset nft
// refuses leaves the firewall as it was, and the file is taken back.
func Allow(ctx context.Context, r Rule) error {
	path := filepath.Join(RulesDir, r.Name+".nft")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s is allowed already (%s)", r, path)
	}
	body := fmt.Sprintf("# construct firewall allow %s, %s\n%s\n", r, time.Now().Format("2006-01-02"), r.Line())
	if err := fsx.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	if err := reload(ctx); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// Deny takes a rule back by name and loads the ruleset without it.
func Deny(ctx context.Context, name string) error {
	path := filepath.Join(RulesDir, filepath.Base(name)+".nft")
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no rule named %s: construct firewall status lists them", name)
		}
		return err
	}
	return reload(ctx)
}

// reload checks the ruleset with the person's files and loads it, as nftables.service does.
func reload(ctx context.Context) error {
	if _, err := nft(ctx, "-c", "-f", Ruleset); err != nil {
		return err
	}
	return program.Run(ctx, "systemctl", "reload", "nftables.service")
}

// nft runs nft and answers its output; a refusal comes with nft's last line (internal/program).
func nft(ctx context.Context, args ...string) ([]byte, error) {
	var out bytes.Buffer
	if err := (program.With{Stdout: &out}).Run(ctx, "nft", args...); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Summary is one line for a report: what was dropped and who scanned this boot, and who is
// held now.
func Summary(counters map[string]Counter, events []Event, blocked []Blocked) string {
	dropped := counters["dropped"].Packets + counters["invalid"].Packets
	scans := map[netip.Addr]bool{}
	for _, e := range events {
		if e.Scan && e.Source.IsValid() {
			scans[e.Source] = true
		}
	}
	s := fmt.Sprintf("%d packets dropped, %d scans this boot", dropped, len(scans))
	if len(blocked) > 0 {
		var addrs []string
		for _, b := range blocked {
			addrs = append(addrs, b.Addr.String())
		}
		s += "; holding " + strings.Join(addrs, ", ")
	}
	return s
}
