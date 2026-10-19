package firewall

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nft's JSON of the loaded ruleset, with a person's rule included: every commented rule's
// counter, by its comment.
func TestParseCounters(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "table.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseCounters(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid", "scanners", "scanners6", "ssh", "allow-8080-tcp", "scans", "scans6", "dropped"} {
		if _, ok := c[name]; !ok {
			t.Errorf("no counter %q in %v", name, c)
		}
	}
	if len(c) != 8 {
		t.Errorf("%d counters: %v", len(c), c)
	}
}

// A set's elements come with what is left of their timeout.
func TestParseScanners(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "scanners.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseScanners(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 1 || s[0].Addr != netip.MustParseAddr("10.99.0.2") || s[0].Expires != 599*time.Second {
		t.Errorf("%+v", s)
	}
	if s, _ := ParseScanners([]byte(`{"nftables": [{"set": {"elem": []}}]}`)); len(s) != 0 {
		t.Errorf("an empty set: %+v", s)
	}
}

// The kernel's log lines, as journalctl -o short-iso prints them: a scan and a drop, with the
// source, the protocol and the port; anything else in the journal is passed over.
func TestParseLog(t *testing.T) {
	lines := `2026-10-19T14:00:01-0300 construct kernel: construct-fw scan: IN=enp0s3 OUT= MAC=52:54:00:12:34:56 SRC=10.99.0.2 DST=10.99.0.1 LEN=60 TOS=0x00 PREC=0x00 TTL=64 ID=1 DF PROTO=TCP SPT=43210 DPT=9003 WINDOW=64240 RES=0x00 SYN URGP=0
2026-10-19T14:00:02-0300 construct kernel: something else entirely
2026-10-19T14:00:03-0300 construct kernel: construct-fw drop: IN=enp0s3 OUT= SRC=2001:db8::7 DST=2001:db8::1 LEN=80 PROTO=UDP SPT=5000 DPT=161 LEN=60
`
	ev := ParseLog(strings.NewReader(lines))
	if len(ev) != 2 {
		t.Fatalf("%d events: %+v", len(ev), ev)
	}
	if !ev[0].Scan || ev[0].Source != netip.MustParseAddr("10.99.0.2") || ev[0].Proto != "tcp" || ev[0].Port != 9003 || ev[0].Time.Hour() != 14 {
		t.Errorf("the scan: %+v", ev[0])
	}
	if ev[1].Scan || ev[1].Source != netip.MustParseAddr("2001:db8::7") || ev[1].Proto != "udp" || ev[1].Port != 161 {
		t.Errorf("the drop: %+v", ev[1])
	}
}

// A rule is named by what it lets in, written as a rule of the input chain counted under that
// name, and read back the same.
func TestRules(t *testing.T) {
	r, err := NewRule(8080, "tcp", "192.168.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "allow-8080-tcp-from-192.168.1.0_24" || r.String() != "8080/tcp from 192.168.1.0/24" {
		t.Errorf("%+v %s", r, r)
	}
	if r.Line() != `ip saddr 192.168.1.0/24 tcp dport 8080 counter accept comment "allow-8080-tcp-from-192.168.1.0_24"` {
		t.Errorf("line: %s", r.Line())
	}
	if back, ok := ParseRule(r.Line()); !ok || back != r {
		t.Errorf("read back: %+v %v", back, ok)
	}
	any, _ := NewRule(53, "udp", "")
	if any.Line() != `udp dport 53 counter accept comment "allow-53-udp"` || any.String() != "53/udp from anyone" {
		t.Errorf("%s / %s", any.Line(), any)
	}
	one, _ := NewRule(22, "tcp", "2001:db8::7")
	if one.Line() != `ip6 saddr 2001:db8::7/128 tcp dport 22 counter accept comment "allow-22-tcp-from-2001_db8__7_128"` {
		t.Errorf("one address: %s", one.Line())
	}
	for _, bad := range [][3]string{{"0", "tcp", ""}, {"80", "icmp", ""}, {"80", "tcp", "home"}} {
		port := 0
		if bad[0] == "80" {
			port = 80
		}
		if _, err := NewRule(port, bad[1], bad[2]); err == nil {
			t.Errorf("%v taken", bad)
		}
	}
	// The files under RulesDir, as a report or status lists them.
	root := t.TempDir()
	dir := filepath.Join(root, RulesDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, x := range []Rule{r, any} {
		if err := os.WriteFile(filepath.Join(dir, x.Name+".nft"), []byte("# by hand\n"+x.Line()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rules, err := Rules(root)
	if err != nil || len(rules) != 2 || rules[0].Name != "allow-53-udp" || rules[1].Name != r.Name {
		t.Errorf("%+v %v", rules, err)
	}
}

func TestSummary(t *testing.T) {
	c := map[string]Counter{"dropped": {Packets: 40}, "invalid": {Packets: 2}}
	ev := []Event{{Scan: true, Source: netip.MustParseAddr("10.99.0.2")}, {Scan: true, Source: netip.MustParseAddr("10.99.0.2")}, {Scan: false, Source: netip.MustParseAddr("10.0.0.9")}}
	got := Summary(c, ev, []Blocked{{Addr: netip.MustParseAddr("10.99.0.2"), Expires: time.Minute}})
	if got != "42 packets dropped, 1 scans this boot; holding 10.99.0.2" {
		t.Error(got)
	}
	if got := Summary(nil, nil, nil); got != "0 packets dropped, 0 scans this boot" {
		t.Error(got)
	}
}
