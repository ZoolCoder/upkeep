// Command upkeep reconciles an app's cloud footprint from a YAML file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zoolcoder/upkeep/internal/cfapi"
	"github.com/zoolcoder/upkeep/internal/config"
	"github.com/zoolcoder/upkeep/internal/engine"
	"github.com/zoolcoder/upkeep/internal/importer"
	"github.com/zoolcoder/upkeep/internal/neonapi"
	"github.com/zoolcoder/upkeep/internal/plan"
	"github.com/zoolcoder/upkeep/internal/renderapi"
	"github.com/zoolcoder/upkeep/internal/status"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `upkeep reconciles an app's cloud footprint from a YAML file.

Usage:
  upkeep plan     [flags]        show what would change (default)
  upkeep apply    [flags] [file] make the changes; with a saved plan, exactly those
  upkeep status   [flags]        what is live right now, without diffing
  upkeep validate [flags]        check the config offline, no network or credentials
  upkeep import   [flags]        print a config block from live state
  upkeep version

Exit codes:
  0  nothing to do, or the apply converged and was verified
  1  something failed, or an applied change is still not in place
  2  plan -exit-code only: changes are outstanding that upkeep can make
  3  plan -exit-code only: nothing left but MANUAL items, which no tool can do

Flags:
  -config string   config file, or "-" for standard input (default "upkeep.yaml")
  -app value       limit to this app; repeat for several
  -deploy          also trigger a Render deploy once the environment converges
  -yes             skip the confirmation prompt for deletions
  -exit-code       plan only: exit 2 when anything differs, so CI can fail on drift
  -json            plan and status: machine-readable output, same redaction rules
  -out string      plan only: save the plan, to apply exactly what you reviewed
  -image string    override the image a -deploy ships, e.g. the tag just built

Import flags (every surface is optional; it imports the ones you name):
  -name string     app name for the imported block (required)
  -account string  Cloudflare account id, for -bucket and -pages
  -render string   Render service id to read the environment from
  -bucket string   R2 bucket to read public access and CORS from
  -pages string    Pages project to read branch and domains from

Environment:
  RENDER_API_KEY         Render; falls back to ~/.render/cli.yaml after ` + "`render login`" + `
  CLOUDFLARE_API_TOKEN   Cloudflare, for R2 and Pages; falls back to the
                         session ` + "`wrangler login`" + ` already stored
  NEON_API_KEY           Neon; falls back to the session ` + "`neonctl auth`" + `
                         already stored

A surface whose credential is absent is reported, never skipped silently.

Values marked ` + "`manual`" + ` in the config, and any whose valueEnv is unset, are
listed as MANUAL and left alone. upkeep never invents a credential, and an
R2 API token cannot be minted from any CLI — naming the gap is the point.
`

type appList []string

func (a *appList) String() string { return strings.Join(*a, ",") }
func (a *appList) Set(v string) error {
	*a = append(*a, v)
	return nil
}

// These are -exit-code's signals, not failures: the plan printed fine.
var (
	errDrift      = errors.New("the live state differs from the config")
	errManualOnly = errors.New("only manual items are outstanding")
)

func main() {
	err := runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	switch {
	case err == nil:
	case errors.Is(err, errDrift):
		os.Exit(2)
	case errors.Is(err, errManualOnly):
		os.Exit(3)
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runCLI is main() without the process. Exported to the package's own tests so
// the wiring — flags, config loading, provider selection, exit paths — is
// exercised as a shell would exercise it.
func runCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}

	switch command {
	case "version":
		_, err := fmt.Fprintln(stdout, version)
		return err
	case "help", "-h", "--help":
		_, err := fmt.Fprint(stdout, usage)
		return err
	case "plan", "apply", "import", "validate", "status":
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	path := flags.String("config", "upkeep.yaml", "config file")
	deploy := flags.Bool("deploy", false, "trigger a Render deploy")
	yes := flags.Bool("yes", false, "skip the deletion prompt")
	exitCode := flags.Bool("exit-code", false, "exit 2 for changes, 3 for manual-only")
	asJSON := flags.Bool("json", false, "machine-readable plan")
	out := flags.String("out", "", "save the plan to this file")
	image := flags.String("image", "", "override the image to deploy")
	var apps appList
	flags.Var(&apps, "app", "limit to this app")
	name := flags.String("name", "", "app name (import)")
	account := flags.String("account", "", "Cloudflare account id (import)")
	service := flags.String("render", "", "Render service id (import)")
	bucket := flags.String("bucket", "", "R2 bucket (import)")
	project := flags.String("pages", "", "Pages project (import)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if command == "import" {
		prov := providers()
		block, err := importer.Import(context.Background(), importer.Request{
			Name: *name, AccountID: *account, ServiceID: *service,
			Bucket: *bucket, Pages: *project,
		}, renderOrNil(prov), cfOrNil(prov))
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, block)
		return nil
	}

	cfg, err := config.LoadFrom(*path, stdin, os.Getenv)
	if err != nil {
		return err
	}

	// validate is the whole point of loading without going further: it touches
	// no network and needs no credential, so it belongs in a pre-commit hook.
	if command == "status" {
		prov := providers()
		selected, err := engine.Select(cfg, apps)
		if err != nil {
			return err
		}
		live := status.Read(context.Background(), selected,
			statusRender(prov), statusCF(prov))
		if *asJSON {
			return status.WriteJSON(stdout, live)
		}
		return status.Write(stdout, live)
	}

	if command == "validate" {
		_, err := fmt.Fprintf(stdout, "%s: %d app(s), valid\n", *path, len(cfg.Apps))
		return err
	}

	// A build pipeline knows the tag it just pushed; the config only knows the
	// moving one. Deploying what was actually built beats deploying whatever
	// :latest points at by the time this runs.
	if *image != "" {
		for i := range cfg.Apps {
			if cfg.Apps[i].Render != nil {
				cfg.Apps[i].Render.Image = *image
			}
		}
	}

	eng := engine.New(cfg, providers(), engine.Options{
		Apps:   apps,
		Deploy: *deploy,
	})

	ctx := context.Background()
	p, err := eng.Plan(ctx)
	if err != nil {
		return err
	}

	if command == "plan" {
		write := p.Write
		if *asJSON {
			write = p.WriteJSON
		}
		if err := write(stdout); err != nil {
			return err
		}
		// Drift is not an error — it is the normal output of a plan — so it
		// only becomes an exit status when a caller asks. CI asks.
		if *out != "" {
			if err := savePlan(p, *out); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "\nsaved to %s — apply it with: upkeep apply %s\n", *out, *out)
		}
		// Drift is not an error — it is the normal output of a plan — so it
		// only becomes an exit status when a caller asks. CI asks.
		//
		// Executable changes and manual ones get different codes because they
		// are different situations. A gate that fails forever on something no
		// tool can do is a gate people turn off, and then it catches nothing.
		if *exitCode {
			switch {
			case len(p.Executable()) > 0:
				return errDrift
			case len(p.Manual()) > 0:
				return errManualOnly
			}
		}
		return nil
	}

	// A saved plan is applied only if the world still looks the way it did
	// when it was reviewed.
	if file := flags.Arg(0); file != "" {
		saved, err := readSavedPlan(file)
		if err != nil {
			return err
		}
		if err := saved.Matches(p); err != nil {
			return err
		}
	}

	if err := p.Write(stdout); err != nil {
		return err
	}
	if len(p.Executable()) == 0 {
		if manual := p.Manual(); len(manual) > 0 {
			fmt.Fprintln(stdout, "\nnothing to apply — everything left is yours to do.")
		}
		return nil
	}
	if p.Destructive() && !*yes && *path == "-" {
		// The config came from standard input, so there is nothing left to
		// answer a prompt with. Saying so beats a prompt that reads no reply
		// and cancels for reasons nobody can see.
		return errors.New("this plan deletes things and the config came from stdin; pass -yes to confirm")
	}
	if p.Destructive() && !*yes {
		ok, err := confirm(stdin, stdout)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled")
		}
	}
	fmt.Fprintln(stdout)
	return eng.Apply(ctx, p, stdout)
}

// providers builds whatever the environment allows. A nil client is not a
// failure here: the engine turns it into a manual action naming the credential,
// which is more useful than refusing to run at all.
func providers() engine.Providers {
	var p engine.Providers
	if key := renderToken(); key != "" {
		p.Render = renderapi.New(os.Getenv("RENDER_API_URL"), key)
	}
	token, note := cloudflareToken()
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	if token != "" {
		p.CF = cfapi.New(os.Getenv("CLOUDFLARE_API_URL"), token)
	}
	neonKey, neonNote := neonToken()
	if neonNote != "" {
		fmt.Fprintln(os.Stderr, neonNote)
	}
	if neonKey != "" {
		p.Neon = neonapi.New(os.Getenv("NEON_API_URL"), neonKey)
	}
	return p
}

// renderToken prefers the environment, then the render CLI's own session, so an
// operator who has run `render login` needs no second long-lived key on disk.
func renderToken() string {
	if key := os.Getenv("RENDER_API_KEY"); key != "" {
		return key
	}
	return renderapi.TokenFromCLI()
}

// cloudflareToken does the same for Cloudflare, borrowing wrangler's session.
//
// The note is not an error. A borrowed session that has expired still needs
// saying out loud, because the alternative is a 401 that reads like a
// permission problem and sends people to their account settings.
func cloudflareToken() (token, note string) {
	if key := os.Getenv("CLOUDFLARE_API_TOKEN"); key != "" {
		return key, ""
	}
	session, found := cfapi.TokenFromWrangler()
	switch {
	case !found:
		return "", ""
	case session.Expired:
		return "", "cloudflare: " + session.ExpiredMessage()
	default:
		return session.Token, ""
	}
}

// neonToken completes the set: every provider upkeep talks to reuses the
// session its own CLI already stored, so the tool works on a machine where the
// CLIs work and asks for no second credential.
func neonToken() (token, note string) {
	if key := os.Getenv("NEON_API_KEY"); key != "" {
		return key, ""
	}
	session, found := neonapi.TokenFromCLI()
	switch {
	case !found:
		return "", ""
	case session.Expired:
		return "", "neon: " + session.ExpiredMessage()
	default:
		return session.Token, ""
	}
}

func confirm(stdin io.Reader, stdout io.Writer) (bool, error) {
	fmt.Fprint(stdout, "\nthis plan deletes things. type yes to continue: ")
	var answer string
	if _, err := fmt.Fscanln(stdin, &answer); err != nil {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(answer), "yes"), nil
}

// The importer takes interfaces, and a nil *Client in an interface is not a nil
// interface — it would pass a nil check and then panic on use.
func renderOrNil(p engine.Providers) importer.Render {
	if p.Render == nil {
		return nil
	}
	return p.Render
}

func cfOrNil(p engine.Providers) importer.CF {
	if p.CF == nil {
		return nil
	}
	return p.CF
}

func savePlan(p plan.Plan, path string) error {
	// 0600: a plan names services and variables, which is not secret but is
	// nobody else's business on a shared machine.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("save plan: %w", err)
	}
	defer func() { _ = f.Close() }()
	return p.Save(f)
}

func readSavedPlan(path string) (plan.Saved, error) {
	f, err := os.Open(path)
	if err != nil {
		return plan.Saved{}, fmt.Errorf("read saved plan: %w", err)
	}
	defer func() { _ = f.Close() }()
	return plan.LoadSaved(f)
}

// A nil *Client in an interface is not a nil interface — it would pass a nil
// check and then panic on use.
func statusRender(p engine.Providers) status.Render {
	if p.Render == nil {
		return nil
	}
	return p.Render
}

func statusCF(p engine.Providers) status.CF {
	if p.CF == nil {
		return nil
	}
	return p.CF
}
