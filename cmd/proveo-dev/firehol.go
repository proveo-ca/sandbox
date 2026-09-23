// SPEC: _spec/_plans/retire-docker-egress.puml, _spec/_plans/host-shell-to-go.puml
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/proveo-ca/proveo/internal/ui"
)

func init() {
	register(&cobra.Command{
		Use:   "firehol-ipsets [output-dir]",
		Short: "Fetch a FireHOL blocklist-ipsets netset and convert it into Squid ACLs",
		Long: `Writes <output-dir>/firehol-ipset.conf (default: $FIREHOL_OUTPUT_DIR, else ./config).

Environment:
  FIREHOL_IPSET        FireHOL ipset name (default: firehol_level1)
  FIREHOL_REF          blocklist-ipsets git ref (default: master)
  FIREHOL_SOURCE_URL   Override source URL
  FIREHOL_SHA256       Expected sha256 of the netset; mismatch refuses
  FIREHOL_OUTPUT_DIR   Output directory when no argument is provided

Notes:
  - This does not install FireHOL or ipset kernel rules.
  - This is a Squid adaptation of a FireHOL IP list.
  - FireHOL docs warn that IP blocklists can have false positives; keep this
    optional and pair it with allowlists where needed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg := fireholConfigFrom(os.Getenv, cwd, args)
			return fireholUpdate(cfg, http.DefaultClient, time.Now)
		},
	})
}

type fireholConfig struct{ ipset, ref, url, sha, outDir string }

func fireholConfigFrom(getenv func(string) string, cwd string, args []string) fireholConfig {
	or := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	c := fireholConfig{ipset: or("FIREHOL_IPSET", "firehol_level1"), ref: or("FIREHOL_REF", "master"), sha: getenv("FIREHOL_SHA256")}
	c.url = or("FIREHOL_SOURCE_URL", "https://raw.githubusercontent.com/firehol/blocklist-ipsets/"+c.ref+"/"+c.ipset+".netset")
	c.outDir = or("FIREHOL_OUTPUT_DIR", filepath.Join(cwd, "config"))
	if len(args) > 0 && args[0] != "" {
		c.outDir = args[0]
	}
	return c
}

func fireholUpdate(c fireholConfig, client *http.Client, now func() time.Time) error {
	if err := os.MkdirAll(c.outDir, 0o755); err != nil {
		return err
	}
	if c.ref == "master" && c.sha == "" {
		ui.Warnf("fetching FireHOL from a mutable 'master' ref with no checksum — set FIREHOL_REF=<commit> and FIREHOL_SHA256=<sum> for a reproducible, verified fetch.")
	}
	resp, err := client.Get(c.url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fetch %s: %s", c.url, resp.Status)
	}
	netset, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if c.sha != "" {
		sum := sha256.Sum256(netset)
		if got := hex.EncodeToString(sum[:]); got != c.sha {
			return fmt.Errorf("FireHOL netset checksum mismatch (expected %s, got %s); refusing", c.sha, got)
		}
	}
	out := filepath.Join(c.outDir, "firehol-ipset.conf")
	if err := os.WriteFile(out, squidACL(c, netset, now().UTC()), 0o644); err != nil {
		return err
	}
	ui.Okf("Wrote %s", out)
	return nil
}

func squidACL(c fireholConfig, netset []byte, at time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Generated from %s\n", c.url)
	fmt.Fprintf(&b, "# FireHOL ipset: %s\n", c.ipset)
	fmt.Fprintf(&b, "# Generated at: %s\n", at.Format("2006-01-02T15:04:05Z"))
	b.WriteString("# Review false-positive risk before enabling broad feeds.\n")
	sc := bufio.NewScanner(bytes.NewReader(netset))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, line)
		if line != "" {
			fmt.Fprintf(&b, "acl firehol_ipset dst %s\n", line)
		}
	}
	b.WriteString("http_access deny firehol_ipset\n")
	return b.Bytes()
}
