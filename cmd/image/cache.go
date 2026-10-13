package main

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The release builder's cache (infra's packages bucket): an EC2 machine that starts empty would
// build every recipe, the kernel with ThinLTO and the harness's QEMU on each release, far past its
// two hours. packages/ is synced as it is; the others are tarballs named by a hash of what they
// are built from, so a change to their inputs is a miss and a rebuild, never a stale hit. It goes
// through the AWS CLI, with the credentials and region a person's or the builder's environment
// gives it.

// cached is a build output the cache keeps: what it is built from (paths git tracks), and what it
// is - directories under the repository, with the file its build writes when it is whole, or a
// Docker image.
type cached struct {
	name   string
	inputs []string
	dirs   []string
	whole  string
	image  string
}

var cacheable = []cached{
	{name: "kernel", inputs: []string{"kernel", "build/versions.env"}, dirs: []string{"_output/kernel", "_output/modules"}, whole: "_output/kernel/bzImage"},
	{name: "firmware", inputs: []string{"firmware", "build/versions.env"}, dirs: []string{"_output/firmware"}, whole: "_output/firmware/firmware.tar"},
	{name: "qemu", inputs: []string{"build/qemu", "build/versions.env"}, image: "construct-qemu"},
}

// isBuilt is whether c's output is here to store: a push after a failed release stores what was
// built before it failed, and nothing that was not.
func isBuilt(c cached) bool {
	if c.image != "" {
		return exec.Command("docker", "image", "inspect", c.image).Run() == nil
	}
	_, err := os.Stat(c.whole)
	return err == nil
}

// cacheKey is a hash of the blobs git has for c's inputs: the committed sources, whatever the
// checkout's dates.
func cacheKey(c cached) (string, error) {
	out, err := exec.Command("git", append([]string{"ls-files", "-s", "--"}, c.inputs...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files %v: %w", c.inputs, err)
	}
	if len(out) == 0 {
		return "", fmt.Errorf("%s: git tracks none of %v", c.name, c.inputs)
	}
	return fmt.Sprintf("%x", sha256.Sum256(out))[:16], nil
}

func cacheCmd(args []string) error {
	fl := flag.NewFlagSet("cache", flag.ExitOnError)
	pull := fl.Bool("pull", false, "fetch packages/ and every cached output there is")
	push := fl.Bool("push", false, "store packages/ and the outputs the cache lacks")
	// The cache's bucket (CONSTRUCT_PACKAGES_BUCKET).
	bucket := os.Getenv("CONSTRUCT_PACKAGES_BUCKET")
	_ = fl.Parse(args)
	if bucket == "" || *pull == *push {
		return errors.New("cache: -pull or -push, and -bucket (CONSTRUCT_PACKAGES_BUCKET)")
	}
	s3 := "s3://" + bucket
	if *pull {
		if err := run("aws", "s3", "sync", "--only-show-errors", "--delete", s3+"/packages/", "packages/"); err != nil {
			return err
		}
	} else {
		// An output is stored under the key of its committed inputs: built from changes not yet
		// committed (cache:seed from a person's checkout), it would be a hit for sources it was
		// not built from. Checked before anything is stored.
		for _, c := range cacheable {
			dirty, err := exec.Command("git", append([]string{"status", "--porcelain", "--"}, c.inputs...)...).Output()
			if err != nil {
				return fmt.Errorf("git status %v: %w", c.inputs, err)
			}
			if len(dirty) > 0 {
				return fmt.Errorf("cache: %s's inputs have changes git has not committed, which its key would not say:\n%s", c.name, dirty)
			}
		}
		if err := run("aws", "s3", "sync", "--only-show-errors", "--delete", "packages/", s3+"/packages/"); err != nil {
			return err
		}
	}
	for _, c := range cacheable {
		key, err := cacheKey(c)
		if err != nil {
			return err
		}
		obj := fmt.Sprintf("%s/cache/%s-%s.tar.zst", s3, c.name, key)
		local := filepath.Join(out, "cache", c.name+".tar.zst")
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return err
		}
		if *pull {
			if run("aws", "s3", "cp", "--only-show-errors", obj, local) != nil {
				fmt.Printf("cache: %s %s: a miss, to build\n", c.name, key)
				continue
			}
			if err := unpackCached(c, local); err != nil {
				return err
			}
			fmt.Printf("cache: %s %s: from the cache\n", c.name, key)
			continue
		}
		if !isBuilt(c) {
			fmt.Printf("cache: %s %s: not built, not stored\n", c.name, key)
			continue
		}
		// A miss is head-object's 404, said on stderr: output keeps it out of the log.
		if _, err := output(exec.Command("aws", "s3api", "head-object", "--bucket", bucket, "--key", strings.TrimPrefix(obj, s3+"/"))); err == nil {
			continue
		}
		if err := packCached(c, local); err != nil {
			return err
		}
		if err := run("aws", "s3", "cp", "--only-show-errors", local, obj); err != nil {
			return err
		}
		fmt.Printf("cache: %s %s: stored\n", c.name, key)
	}
	return nil
}

func packCached(c cached, local string) error {
	if c.image != "" {
		return shell(fmt.Sprintf("docker save %s | zstd -q -T0 -o %s", c.image, local))
	}
	return run("tar", append([]string{"-I", "zstd -T0", "-cf", local}, c.dirs...)...)
}

func unpackCached(c cached, local string) error {
	if c.image != "" {
		return shell(fmt.Sprintf("zstd -dc %s | docker load -q", local))
	}
	for _, d := range c.dirs {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return run("tar", "-I", "zstd", "-xf", local)
}

func shell(script string) error {
	cmd := exec.Command("bash", "-c", "set -o pipefail; "+script)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
