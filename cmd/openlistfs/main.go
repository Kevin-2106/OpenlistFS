// Command openlistfs is a read-only, video-playback-oriented OpenList
// filesystem client for Windows (performance PoC).
//
// Phase 0 commands verify the OpenList native API + authentication:
//
//	openlistfs probe
//	openlistfs ls /Movies
//	openlistfs stat /Movies/A.mkv
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	cfgpkg "openlistfs/internal/config"
	"openlistfs/internal/openlist"
	"openlistfs/internal/proxy"
	"openlistfs/internal/vpath"
)

const usage = `openlistfs — read-only OpenList filesystem client (performance PoC)

Usage:

  openlistfs probe              Check server reachability and authentication
  openlistfs ls <path>          List a directory
  openlistfs stat <path>        Show metadata for one path
  openlistfs readtest <path>    HTTP Range benchmark against the direct link
                                [--offset N] [--length N] [--stride N] [--count N]
  openlistfs mount O:           mount the read-only view at a drive letter (WinFsp)
                                [--proxy none|system|http://host:port|socks5://host:port]

Configuration (environment or .env, see .env.example):

  OPENLISTFS_URL                e.g. https://example.com
  OPENLISTFS_TOKEN              an API token, or
  OPENLISTFS_USERNAME / OPENLISTFS_PASSWORD
  OPENLISTFS_PROXY              proxy for all traffic: "none" (default), "system",
                                or a URL like http://127.0.0.1:7897 (mount's
                                --proxy flag overrides)

openlistfs is currently a read-only performance proof-of-concept.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "probe":
		err = cmdProbe(ctx)
	case "ls":
		err = cmdLs(ctx, os.Args[2:])
	case "stat":
		err = cmdStat(ctx, os.Args[2:])
	case "mount":
		err = cmdMount(ctx, os.Args[2:])
	case "readtest":
		err = cmdReadtest(ctx, os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func newClient(ctx context.Context) (*openlist.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return buildClient(ctx, cfg)
}

func buildClient(ctx context.Context, cfg cfgpkg.Config) (*openlist.Client, error) {
	opts, err := clientOptions(cfg)
	if err != nil {
		return nil, err
	}
	c := openlist.New(cfg.URL, opts...)
	if cfg.Token == "" && cfg.Username != "" {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// clientOptions assembles the option set every client construction site
// (buildClient, probe) shares — including OPENLISTFS_PROXY routing. An
// invalid proxy setting is an error, never a silent direct fallback.
func clientOptions(cfg cfgpkg.Config) ([]openlist.Option, error) {
	var opts []openlist.Option
	if cfg.Token != "" {
		opts = append(opts, openlist.WithToken(cfg.Token))
	}
	if cfg.Username != "" {
		opts = append(opts, openlist.WithCredentials(cfg.Username, cfg.Password))
	}
	if cfg.Proxy != "" {
		fn, _, err := proxy.Parse(cfg.Proxy)
		if err != nil {
			return nil, err
		}
		opts = append(opts, openlist.WithTransport(openlist.NewTransport(fn)))
	}
	return opts, nil
}

func cmdProbe(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	opts, err := clientOptions(cfg)
	if err != nil {
		return err
	}
	c := openlist.New(cfg.URL, opts...)

	start := time.Now()
	info, err := c.Probe(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("server:   reachable in %s\n", time.Since(start).Round(time.Millisecond))
	if info.Title != "" {
		fmt.Printf("title:    %s\n", info.Title)
	}
	if info.Version != "" {
		fmt.Printf("version:  %s\n", info.Version)
	}

	// Authentication check: log in if needed, then stat the root.
	auth := "anonymous (no credentials configured)"
	switch {
	case cfg.Token != "":
		auth = "token from OPENLISTFS_TOKEN"
	case cfg.Username != "":
		start = time.Now()
		if err := c.Login(ctx); err != nil {
			return fmt.Errorf("login: %w", err)
		}
		auth = fmt.Sprintf("logged in as %q in %s", cfg.Username, time.Since(start).Round(time.Millisecond))
	}
	if _, err := c.Stat(ctx, "/"); err != nil {
		return fmt.Errorf("root stat after auth (%s): %w", auth, err)
	}
	fmt.Printf("auth:     ok — %s\n", auth)
	fmt.Printf("root:     stat / ok\n")
	return nil
}

func cmdLs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	long := fs.Bool("l", false, "print full metadata as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p := "/"
	if fs.NArg() > 0 {
		p = vpath.Clean(fs.Arg(0))
	}
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	entries, err := c.List(ctx, p)
	if err != nil {
		return err
	}
	if *long {
		return json.NewEncoder(os.Stdout).Encode(entries)
	}
	for _, e := range entries {
		kind := "-"
		if e.IsDir {
			kind = "d"
		}
		mtime := time.Unix(e.Modified, 0).Format("2006-01-02 15:04")
		fmt.Printf("%s %10s  %s  %s\n", kind, fmtBytes(e.Size), mtime, e.Name)
	}
	fmt.Printf("(%d entries)\n", len(entries))
	return nil
}

func cmdStat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stat", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openlistfs stat <path>")
	}
	p := vpath.Clean(fs.Arg(0))
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	e, err := c.Stat(ctx, p)
	if err != nil {
		return err
	}
	kind := "file"
	if e.IsDir {
		kind = "directory"
	}
	fmt.Printf("path:      %s\n", p)
	fmt.Printf("kind:      %s\n", kind)
	fmt.Printf("size:      %d (%s)\n", e.Size, fmtBytes(e.Size))
	fmt.Printf("modified:  %s\n", time.Unix(e.Modified, 0).Format(time.RFC3339))
	if e.Sign != "" {
		fmt.Printf("sign:      %s\n", redactMiddle(e.Sign, 6))
	}
	return nil
}

func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// redactMiddle keeps only a short prefix and suffix of a capability string.
func redactMiddle(s string, keep int) string {
	if len(s) <= keep*2 {
		return s
	}
	return s[:keep] + "…" + s[len(s)-2:]
}
