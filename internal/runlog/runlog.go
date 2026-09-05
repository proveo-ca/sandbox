// Package runlog gives every run a transcript under PROVEO_HOME/logs so a
// failure can be diagnosed after the fact.
//
// SPEC: _spec/internal/runlog/run-transcript.puml
package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/proveo-ca/proveo/internal/proveohome"
)

const keep = 20

// Log is an open transcript.
type Log struct {
	f    *os.File
	path string
}

func Open(sid string) (*Log, error) {
	dir := filepath.Join(proveohome.Root(os.Getenv), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	prune(dir)

	path := filepath.Join(dir, sid+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	link := filepath.Join(dir, "latest.log")
	_ = os.Remove(link)
	_ = os.Symlink(path, link)

	l := &Log{f: f, path: path}
	l.Section("run " + sid)
	return l, nil
}

// Path is the transcript's location, for telling the operator where to look.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Writer exposes the file for ui.TeeTo.
func (l *Log) Writer() *os.File {
	if l == nil {
		return nil
	}
	return l.f
}

// Printf appends one timestamped line.
func (l *Log) Printf(format string, a ...any) {
	if l == nil || l.f == nil {
		return
	}
	fmt.Fprintf(l.f, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, a...))
}

// Section starts a labelled block, so a transcript can be skimmed.
func (l *Log) Section(name string) {
	if l == nil || l.f == nil {
		return
	}
	fmt.Fprintf(l.f, "\n=== %s === %s\n", name, time.Now().Format(time.RFC3339))
}

func (l *Log) Fields(section string, kv map[string]string) {
	if l == nil || l.f == nil {
		return
	}
	l.Section(section)
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := kv[k]
		if v == "" {
			v = "(unset)"
		}
		fmt.Fprintf(l.f, "  %-22s %s\n", k, v)
	}
}

const PolicyLogFile = "policy-log.json"

// Artifacts records where the evidence for this run lives.
func (l *Log) Artifacts(egDir string, sandboxed bool) {
	if l == nil || l.f == nil || egDir == "" {
		return
	}
	if sandboxed {
		l.Fields("artifacts", map[string]string{
			"session state": egDir,
			"sandbox kit":   filepath.Join(egDir, "sbx", "kit", "spec.yaml"),
			"policy log":    filepath.Join(egDir, "sbx", PolicyLogFile),
			"enforcement":   "sbx — see `sbx policy log <sandbox>`, `sbx diagnose`; proveo runs no Squid or MITM here",
		})
		return
	}
	l.Fields("artifacts", map[string]string{
		"egress state":  egDir,
		"flow record":   filepath.Join(egDir, "flows.ndjson"),
		"squid access":  filepath.Join(egDir, "squid", "logs", "access.log"),
		"squid cache":   filepath.Join(egDir, "squid", "logs", "cache.log"),
		"inspector log": filepath.Join(egDir, "inspector.log"),
	})
}

// Close flushes and closes the transcript.
func (l *Log) Close() {
	if l == nil || l.f == nil {
		return
	}
	l.Section("end")
	_ = l.f.Close()
	l.f = nil
}

func prune(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") && e.Name() != "latest.log" {
			logs = append(logs, e.Name())
		}
	}
	sort.Strings(logs)
	for i := 0; i < len(logs)-keep+1 && i < len(logs); i++ {
		_ = os.Remove(filepath.Join(dir, logs[i]))
	}
}
