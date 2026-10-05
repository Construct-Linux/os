package main

import (
	"os"
	"regexp"
	"testing"
)

// The builder installs build/host.env's Go (build/host-tools.sh); go.mod asks for the same one, a
// full version, which GOTOOLCHAIN can fetch - a bare "1.27" is no toolchain's name.
func TestHostGoIsGoMod(t *testing.T) {
	env, err := os.ReadFile("../../build/host.env")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	host := regexp.MustCompile(`(?m)^GO_VERSION=(\S+)$`).FindSubmatch(env)
	gomod := regexp.MustCompile(`(?m)^go (\S+)$`).FindSubmatch(mod)
	if host == nil || gomod == nil || string(host[1]) != string(gomod[1]) {
		t.Errorf("host.env's Go %q, go.mod's %q: want one version", host, gomod)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).Match(gomod[1]) {
		t.Errorf("go.mod's go %s is not a full version", gomod[1])
	}
}
