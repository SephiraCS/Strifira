package machine

import (
	"errors"
	"slices"
	"strings"
)

func isolatedGitCommand(script, name string, args ...string) []string {
	command := []string{"/usr/bin/env", "-i", "PATH=/usr/bin:/bin", "HOME=/tmp", "LANG=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false",
		"/bin/sh", "-c", script, name}
	return append(command, args...)
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func gitPathRecords(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	if !strings.HasSuffix(value, "\x00") {
		return nil, errors.New("Git path listing is incomplete")
	}
	paths := strings.Split(strings.TrimSuffix(value, "\x00"), "\x00")
	for _, path := range paths {
		if path == "" {
			return nil, errors.New("Git path listing contains an empty record")
		}
	}
	return paths, nil
}

func parseGitSnapshot(output string) (GitState, error) {
	parts := strings.SplitN(output, "\x00", 4)
	if len(parts) != 4 || parts[0] == "" || parts[2] != "true" && parts[2] != "false" {
		return GitState{}, errors.New("Git snapshot metadata is incomplete")
	}
	commits := nonEmptyLines(parts[1])
	for _, commit := range commits {
		if !validGitObjectID(commit) {
			return GitState{}, errors.New("Git commit listing is invalid")
		}
	}
	files, err := gitPathRecords(parts[3])
	if err != nil {
		return GitState{}, err
	}
	slices.Sort(files)
	return GitState{Branch: parts[0], Commits: commits, ChangedFiles: slices.Compact(files), Dirty: parts[2] == "true"}, nil
}

// Docker's subpath bind mounts are separate mount points even on one volume.
// Use the backend-only common mount for cross-subdirectory atomic renames.
const atomicVolumeRenameFunctions = `
atomic_volume_path() {
  case "$1" in
    /workspace|/workspace/*) printf '%s' "/volume/workspace${1#/workspace}" ;;
    /repositories|/repositories/*) printf '%s' "/volume/repositories${1#/repositories}" ;;
    *) printf '%s' "$1" ;;
  esac
}
atomic_volume_move() {
  policy=$1; shift
  from=$(atomic_volume_path "$1"; printf .); from=${from%.}
  to=$(atomic_volume_path "$2"; printf .); to=${to%.}
  mv -T "$policy" -- "$from" "$to"
}
`
