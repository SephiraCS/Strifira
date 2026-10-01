// check-ci-timeouts keeps cancellation cleanup older than every workflow job.
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func check(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var workflow struct {
		Jobs map[string]struct {
			Timeout *int `yaml:"timeout-minutes"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return err
	}
	if len(workflow.Jobs) == 0 {
		return fmt.Errorf("workflow has no jobs")
	}
	for name, job := range workflow.Jobs {
		if job.Timeout == nil || *job.Timeout <= 0 || *job.Timeout >= 360 {
			return fmt.Errorf("job %s must declare timeout-minutes between 1 and 359", name)
		}
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: check-ci-timeouts WORKFLOW...")
		os.Exit(2)
	}
	failed := false
	for _, path := range os.Args[1:] {
		if err := check(path); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}
