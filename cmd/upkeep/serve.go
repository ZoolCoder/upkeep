package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zoolcoder/upkeep/internal/web"
	"github.com/zoolcoder/zcadmin"
	"github.com/zoolcoder/zcadmin/auth"
)

// runServe is `upkeep serve`: the admin page on loopback. It shares every
// token helper with the commands, so the page sees exactly the providers the
// terminal would.
func runServe(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	addr := flags.String("addr", "127.0.0.1:7778", "listen address")
	path := flags.String("config", "upkeep.yaml", "config file")
	data := flags.String("data", "", "directory for the password hash and activity log")
	insecure := flags.Bool("insecure", false, "allow a non-loopback address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := loopbackOnly(*addr, *insecure); err != nil {
		return err
	}
	opts := web.Options{ConfigPath: *path, Providers: providers, Credentials: credentials}
	if *data != "" {
		opts.AuthFile = filepath.Join(*data, "auth.json")
		opts.ActivityFile = filepath.Join(*data, "activity.jsonl")
	} else {
		opts.AuthFile = auth.DefaultFile("upkeep")
		opts.ActivityFile = zcadmin.DefaultActivityFile("upkeep")
	}
	handler := web.New(opts)
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "upkeep admin on http://%s — Ctrl-C to stop\n", ln.Addr())
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

// loopbackOnly refuses to put the page, and the credentials behind it, on an
// address anyone else can reach — unless told in so many words.
func loopbackOnly(addr string, insecure bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-addr %q: %v", addr, err)
	}
	if insecure || host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("-addr %q is not loopback; the page fronts your cloud credentials — pass -insecure if you mean it", addr)
}

// credentials is Settings' view of the same lookups providers() makes: which
// provider has a credential and where it came from. Never a value.
func credentials() []web.Credential {
	cf := web.Credential{Provider: "Cloudflare", Source: fromEnvOr("CLOUDFLARE_API_TOKEN", "wrangler login")}
	if token, note := cloudflareToken(); token != "" {
		cf.Present = true
	} else if note != "" {
		cf.Source = note
	}
	neon := web.Credential{Provider: "Neon", Source: fromEnvOr("NEON_API_KEY", "neonctl auth")}
	if token, note := neonToken(); token != "" {
		neon.Present = true
	} else if note != "" {
		neon.Source = note
	}
	return []web.Credential{
		{Provider: "Render", Present: renderToken() != "", Source: fromEnvOr("RENDER_API_KEY", "render login")},
		cf,
		neon,
		{Provider: "Fly", Present: flyToken() != "", Source: fromEnvOr("FLY_API_TOKEN", "fly auth login")},
	}
}

// fromEnvOr names the source a credential is looked up in: the variable when
// it is set, otherwise the CLI session it would be borrowed from.
func fromEnvOr(env, cli string) string {
	if os.Getenv(env) != "" {
		return env
	}
	return cli
}
