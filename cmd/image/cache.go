package main

import (
	"crypto/sha256"
	"errors"
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

// cached is a build output the cache keeps: what it is built from - paths git tracks, and the
// pins of build/versions.env its build reads, not the whole file, so that a scanner's or another
// build's bump does not evict it - and what it is: directories under the repository, with the
// file its build writes when it is whole, or a Docker image.
type cached struct {
	name   string
	inputs []string
	pins   []string
	dirs   []string
	whole  string
	image  string
}

// pinsFile is where the pins are; a change to it that touches none of a build's pins keeps the
// build's key.
const pinsFile = "build/versions.env"

var cacheable = []cached{
	{name: "kernel", inputs: []string{"kernel"}, pins: []string{"WOLFI_BASE_IMAGE", "KERNEL_VERSION", "KERNEL_SHA256"},
		dirs: []string{"_output/kernel", "_output/modules"}, whole: "_output/kernel/bzImage"},
	{name: "firmware", inputs: []string{"firmware"}, pins: []string{"WOLFI_BASE_IMAGE", "LINUX_FIRMWARE_VERSION", "LINUX_FIRMWARE_SHA256", "INTEL_MICROCODE_VERSION", "INTEL_MICROCODE_SHA256"},
		dirs: []string{"_output/firmware"}, whole: "_output/firmware/firmware.tar"},
	{name: "qemu", inputs: []string{"build/qemu"}, pins: []string{"HARNESS_DEBIAN_IMAGE", "QEMU_VERSION", "QEMU_SHA256", "CANOKEY_QEMU_COMMIT"}, image: "construct-qemu"},
}

// inBucket is whether key is in bucket: head-object's 404, said on stderr (output keeps it out of
// the log), is a miss. Anything else - credentials gone, the wrong region - is an error, not a miss
// that builds the kernel again inside the builder's two hours.
func inBucket(bucket, key string) (bool, error) {
	_, err := output(exec.Command("aws", "s3api", "head-object", "--bucket", bucket, "--key", key))
	switch {
	case err == nil:
		return true, nil
	case strings.Contains(err.Error(), "(404)"):
		return false, nil
	}
	return false, err
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

// cacheKey is a hash of the blobs git has for c's inputs - the committed sources, whatever the
// checkout's dates - and of c's pins as committed.
func cacheKey(c cached) (string, error) {
	out, err := exec.Command("git", append([]string{"ls-files", "-s", "--"}, c.inputs...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files %v: %w", c.inputs, err)
	}
	if len(out) == 0 {
		return "", fmt.Errorf("%s: git tracks none of %v", c.name, c.inputs)
	}
	env, err := exec.Command("git", "show", "HEAD:"+pinsFile).Output()
	if err != nil {
		return "", fmt.Errorf("git show HEAD:%s: %w", pinsFile, err)
	}
	lines, err := pinLines(string(env), c.pins)
	if err != nil {
		return "", fmt.Errorf("%s: %w", c.name, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(append(out, strings.Join(lines, "\n")...)))[:16], nil
}

// pinLines are the lines of env that set pins, in pins' order; a pin env does not set is an
// error, not a key that silently says nothing of it.
func pinLines(env string, pins []string) ([]string, error) {
	var out []string
	for _, pin := range pins {
		found := ""
		for _, line := range strings.Split(env, "\n") {
			if strings.HasPrefix(line, pin+"=") {
				found = line
			}
		}
		if found == "" {
			return nil, fmt.Errorf("%s sets no %s", pinsFile, pin)
		}
		out = append(out, found)
	}
	return out, nil
}

// cacheCmd is image cache pull, which fetches packages/ and every cached output there is, or
// image cache push, which stores packages/ and the outputs the cache lacks, in the bucket
// CONSTRUCT_PACKAGES_BUCKET names.
func cacheCmd(args []string) error {
	bucket := os.Getenv("CONSTRUCT_PACKAGES_BUCKET")
	if len(args) != 1 || (args[0] != "pull" && args[0] != "push") || bucket == "" {
		return errors.New("cache pull|push, with CONSTRUCT_PACKAGES_BUCKET set")
	}
	pull := args[0] == "pull"
	s3 := "s3://" + bucket
	if pull {
		if err := run("aws", "s3", "sync", "--only-show-errors", "--delete", s3+"/packages/", "packages/"); err != nil {
			return err
		}
	} else {
		// An output is stored under the key of its committed inputs: built from changes not yet
		// committed (cache:seed from a person's checkout), it would be a hit for sources it was
		// not built from. Checked before anything is stored.
		for _, c := range cacheable {
			inputs := append([]string{pinsFile}, c.inputs...)
			dirty, err := exec.Command("git", append([]string{"status", "--porcelain", "--"}, inputs...)...).Output()
			if err != nil {
				return fmt.Errorf("git status %v: %w", inputs, err)
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
		stored, err := inBucket(bucket, strings.TrimPrefix(obj, s3+"/"))
		if err != nil {
			return err
		}
		if pull {
			if !stored {
				fmt.Printf("cache: %s %s: a miss, to build\n", c.name, key)
				continue
			}
			if err := run("aws", "s3", "cp", "--only-show-errors", obj, local); err != nil {
				return err
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
		if stored {
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
		return pipeline("", local, nil, []string{"docker", "save", c.image}, []string{"zstd", "-q", "-T0", "-c"})
	}
	return run("tar", append([]string{"-I", "zstd -T0", "-cf", local}, c.dirs...)...)
}

func unpackCached(c cached, local string) error {
	if c.image != "" {
		// docker load decompresses a zstd archive itself.
		return run("docker", "load", "-q", "-i", local)
	}
	for _, d := range c.dirs {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return run("tar", "-I", "zstd", "-xf", local)
}
