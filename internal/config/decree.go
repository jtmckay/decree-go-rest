package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MinDecreeVersion is the oldest decree decree-go-rest works with.
var MinDecreeVersion = [2]int{0, 5}

// decreeTimeout bounds each decree command run during validation.
const decreeTimeout = 30 * time.Second

// DecreeBinary resolves `decree`: a bare name is looked up on PATH, and a
// relative path is taken relative to the config file, like `project`.
func (c *Config) DecreeBinary() string {
	d := c.Decree
	if strings.ContainsRune(d, filepath.Separator) && !filepath.IsAbs(d) {
		return filepath.Join(c.Dir, d)
	}
	return d
}

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// ParseDecreeVersion extracts major and minor from `decree --version`
// output such as "decree 0.5.0".
func ParseDecreeVersion(out string) (major, minor int, err error) {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, fmt.Errorf("no version in %q", strings.TrimSpace(out))
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, nil
}

// checkReport is `decree check --format json` (schema/v1/cli/check.schema.json).
type checkReport struct {
	Valid  *bool `json:"valid"`
	Errors []struct {
		Rule    *string `json:"rule"`
		File    string  `json:"file"`
		Line    int     `json:"line"`
		State   string  `json:"state"`
		Message string  `json:"message"`
	} `json:"errors"`
}

// decree is step 7.
func (v *validator) decree() {
	bin := v.c.DecreeBinary()
	path, err := exec.LookPath(bin)
	if err != nil {
		v.add("", RuleDecree, "cannot find %s: %v", bin, err)
		return
	}

	out, _, err := runDecree(path, v.c.Dir, "--version")
	if err != nil {
		v.add("", RuleDecree, "%s --version: %v", bin, err)
		return
	}
	major, minor, err := ParseDecreeVersion(out)
	if err != nil {
		v.add("", RuleDecree, "%s --version: %v", bin, err)
		return
	}
	if major < MinDecreeVersion[0] || (major == MinDecreeVersion[0] && minor < MinDecreeVersion[1]) {
		v.add("", RuleDecree, "%s is version %d.%d, %d.%d or newer is required", bin, major, minor, MinDecreeVersion[0], MinDecreeVersion[1])
		return
	}

	if !v.hasDecreeDir {
		// Reported already; and decree would look for .decree/ in a parent.
		return
	}
	out, stderr, err := runDecree(path, v.c.ProjectDir, "check", "--format", "json")
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		v.add("", RuleDecree, "%s check: %v", bin, err)
		return
	}
	var rep checkReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil || rep.Valid == nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("unreadable output %q", strings.TrimSpace(out))
		}
		v.add("", RuleDecree, "%s check in %s failed: %s", bin, v.c.ProjectDir, msg)
		return
	}
	if *rep.Valid {
		return
	}
	if len(rep.Errors) == 0 {
		v.add("", RuleDecree, "%s check in %s reports the project invalid", bin, v.c.ProjectDir)
	}
	for _, e := range rep.Errors {
		where := e.File
		if e.Line > 0 {
			where += fmt.Sprintf(": line %d", e.Line)
		} else if e.State != "" {
			where += ": " + e.State
		}
		rule := ""
		if e.Rule != nil {
			rule = " (" + *e.Rule + ")"
		}
		v.add("", RuleDecree, "check: %s: %s%s", where, e.Message, rule)
	}
}

// runDecree runs decree in dir and returns its stdout and stderr. A
// non-zero exit is an *exec.ExitError.
func runDecree(path, dir string, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), decreeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", decreeTimeout)
	}
	return o.String(), e.String(), err
}
