package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A builder writes its log to the logs bucket as it goes (infra builder/user-data.sh): builder.log
// every few seconds, each recipe's build log under recipes/, and the reports when the job ends,
// under s3://construct-builds-<account>/<date>-<instance>/. release-log follows one of them from a
// person's machine, as tail -f would, with no shell on the builder.

// builderRun is a builder instance as release-log finds it.
type builderRun struct {
	ID       string    `json:"id"`
	Launched time.Time `json:"launched"`
	State    string    `json:"state"`
}

func releaseLog(args []string) error {
	fl := flag.NewFlagSet("release-log", flag.ExitOnError)
	id := fl.String("id", "", "the builder instance; the newest one without")
	file := fl.String("file", "builder.log", "the file under the build's prefix: builder.log, recipes/<recipe>.log, logs/<log>")
	follow := fl.Bool("f", true, "follow the file until the job ends")
	_ = fl.Parse(args)

	run, err := findBuilder(*id)
	if err != nil {
		return err
	}
	account, err := output(exec.Command("aws", "sts", "get-caller-identity", "--query", "Account", "--output", "text"))
	if err != nil {
		return err
	}
	bucket := "construct-builds-" + account
	prefix, err := buildPrefix(bucket, run)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "release-log: %s (%s, launched %s): s3://%s/%s%s\n", run.ID, run.State, run.Launched.Format(time.RFC3339), bucket, prefix, *file)

	tmp, err := os.MkdirTemp("", "release-log")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	part := filepath.Join(tmp, "part")
	var offset int64
	// printNew prints what was written since the last read: a range past the end is no new bytes.
	printNew := func() error {
		if _, err := output(exec.Command("aws", "s3api", "get-object", "--bucket", bucket, "--key", prefix+*file,
			"--range", fmt.Sprintf("bytes=%d-", offset), part)); err != nil {
			return nil
		}
		b, err := os.ReadFile(part)
		if err != nil {
			return err
		}
		os.Stdout.Write(b)
		offset += int64(len(b))
		return nil
	}
	for {
		if err := printNew(); err != nil {
			return err
		}
		if !*follow {
			return nil
		}
		// The job's exit status is the last thing a builder writes: once it is there, so is the log.
		if rc, err := output(exec.Command("aws", "s3", "cp", "--quiet", "s3://"+bucket+"/"+prefix+"rc", "-")); err == nil {
			if err := printNew(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "release-log: the job ended with %s\n", strings.TrimSpace(rc))
			if strings.TrimSpace(rc) != "0" {
				return errors.New("the release failed")
			}
			return nil
		}
		time.Sleep(10 * time.Second)
	}
}

// findBuilder is the builder id names, or the one launched last.
func findBuilder(id string) (builderRun, error) {
	q := []string{"ec2", "describe-instances", "--output", "json",
		"--query", "Reservations[].Instances[].{id: InstanceId, launched: LaunchTime, state: State.Name}"}
	if id != "" {
		q = append(q, "--instance-ids", id)
	} else {
		q = append(q, "--filters", "Name=tag:construct:role,Values=builder")
	}
	out, err := output(exec.Command("aws", q...))
	if err != nil {
		return builderRun{}, err
	}
	var runs []builderRun
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		return builderRun{}, fmt.Errorf("describe-instances: %w", err)
	}
	if len(runs) == 0 {
		return builderRun{}, errors.New("no builder: task release launches one")
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Launched.After(runs[j].Launched) })
	return runs[0], nil
}

// buildPrefix is where a builder writes, <date>-<instance>/: the date is the builder's own, taken
// moments after its launch, so the day after the launch is looked at too.
func buildPrefix(bucket string, run builderRun) (string, error) {
	for _, day := range []time.Time{run.Launched, run.Launched.Add(24 * time.Hour)} {
		p := day.UTC().Format("20060102") + "-" + run.ID + "/"
		if out, err := output(exec.Command("aws", "s3api", "list-objects-v2", "--bucket", bucket, "--prefix", p, "--max-keys", "1",
			"--query", "KeyCount", "--output", "text")); err == nil && strings.TrimSpace(out) != "0" {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s has written nothing to s3://%s yet: it uploads its log every 15 seconds once it runs", run.ID, bucket)
}
