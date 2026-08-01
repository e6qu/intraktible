// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/e6qu/intraktible/client"
	"github.com/e6qu/intraktible/providers/domain"
)

type providersConnectionFlags struct {
	serverURL *string
	apiKey    *string
}

func bindProvidersConnection(fs *flag.FlagSet) providersConnectionFlags {
	return providersConnectionFlags{
		serverURL: fs.String("server", "http://localhost:8080", "intraktible server URL"),
		apiKey:    fs.String("api-key", os.Getenv("INTRAKTIBLE_API_KEY"), "API key"),
	}
}

func (flags providersConnectionFlags) client() *client.Client {
	return client.New(*flags.serverURL, *flags.apiKey)
}

func providersCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(
			"providers: command required (install|list|get|configure|test|approve|deploy|" +
				"pause|resume|upgrade|retire|health)",
		)
	}
	switch args[0] {
	case "install":
		return providersInstall(args[1:])
	case "list":
		return providersList(args[1:])
	case "get":
		return providersGet(args[1:])
	case "configure", "test", "approve", "deploy", "pause", "resume", "upgrade", "retire":
		return providersAction(args[0], args[1:])
	case "health":
		return providersHealth(args[1:])
	default:
		return fmt.Errorf("providers: unknown command %q", args[0])
	}
}

func providersInstall(args []string) error {
	fs := flag.NewFlagSet("providers install", flag.ContinueOnError)
	connection := bindProvidersConnection(fs)
	name := fs.String("name", "", "provider name (URL-safe slug)")
	connector := fs.String("connector", "", "backing connector type")
	description := fs.String("description", "", "provider description")
	schema := fs.String("schema", "", "JSON schema the provider returns")
	timeout := fs.Int("timeout-seconds", 10, "per-fetch timeout (seconds)")
	maxRetries := fs.Int("max-retries", 0, "max automatic retries (0 = none)")
	cost := fs.Float64("cost-per-fetch-usd", 0, "per-invocation cost (USD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *connector == "" || *description == "" || *schema == "" {
		return errors.New("providers install: --name, --connector, --description, and --schema are required")
	}
	accepted, err := connection.client().InstallProvider(
		context.Background(), *name, *connector, *description, domain.Conformance{
			Schema: *schema, TimeoutSeconds: *timeout, MaxRetries: *maxRetries, CostPerFetchUSD: *cost,
		},
	)
	if err != nil {
		return err
	}
	return printJSON(accepted)
}

func providersList(args []string) error {
	fs := flag.NewFlagSet("providers list", flag.ContinueOnError)
	connection := bindProvidersConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListProviders(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func providersGet(args []string) error {
	return providersRead(args, "providers get",
		func(c *client.Client, name string, version int) (any, error) {
			return c.GetProvider(context.Background(), name, version)
		})
}

// providersRead implements the shared "--name + --version required, then one client
// read, then print" shape for provider reads.
func providersRead(
	args []string,
	command string,
	read func(c *client.Client, name string, version int) (any, error),
) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	name := fs.String("name", "", "provider name")
	version := fs.Int("version", 0, "provider version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *version < 1 {
		return fmt.Errorf("%s: --name and positive --version are required", command)
	}
	connection := bindProvidersConnection(fs)
	item, err := read(connection.client(), *name, *version)
	if err != nil {
		return err
	}
	return printJSON(item)
}

func providersHealth(args []string) error {
	fs := flag.NewFlagSet("providers health", flag.ContinueOnError)
	connection := bindProvidersConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ProviderHealth(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func providersAction(command string, args []string) error {
	fs := flag.NewFlagSet("providers "+command, flag.ContinueOnError)
	connection := bindProvidersConnection(fs)
	name := fs.String("name", "", "provider name")
	version := fs.Int("version", 0, "provider version")
	environment := fs.String("environment", "", "environment (sandbox|staging|production)")
	reason := fs.String("reason", "", "reason (approve/pause/retire)")
	requestID := fs.String("request-id", "", "approval request id (approve)")
	toVersion := fs.Int("to-version", 0, "target version (upgrade)")
	fixture := fs.String("fixture", "sandbox", "test fixture (test)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("providers %s: --name is required", command)
	}
	env := domain.Environment(*environment)
	c := connection.client()
	ctx := context.Background()
	var result any
	var err error
	switch command {
	case "configure":
		if *version < 1 || !env.Valid() {
			return errors.New("providers configure: positive --version and --environment are required")
		}
		result, err = c.ConfigureProvider(ctx, *name, *version, domain.Configuration{
			Environment: env, Config: map[string]any{},
		})
	case "test":
		if *version < 1 {
			return errors.New("providers test: positive --version is required")
		}
		result, err = c.TestProvider(ctx, *name, *version, domain.TestEvidence{
			Passed: true, Fixture: *fixture,
		})
	case "approve":
		if *version < 1 || *requestID == "" || *reason == "" {
			return errors.New("providers approve: positive --version, --request-id, and --reason are required")
		}
		result, err = c.ApproveProvider(ctx, *name, *version, *requestID, *reason)
	case "deploy":
		if *version < 1 || !env.Valid() {
			return errors.New("providers deploy: positive --version and --environment are required")
		}
		result, err = c.DeployProvider(ctx, *name, *version, env)
	case "pause":
		if *version < 1 || !env.Valid() || *reason == "" {
			return errors.New("providers pause: positive --version, --environment, and --reason are required")
		}
		result, err = c.PauseProvider(ctx, *name, *version, env, *reason)
	case "resume":
		if *version < 1 || !env.Valid() {
			return errors.New("providers resume: positive --version and --environment are required")
		}
		result, err = c.ResumeProvider(ctx, *name, *version, env)
	case "upgrade":
		if *toVersion < 1 || !env.Valid() {
			return errors.New("providers upgrade: positive --to-version and --environment are required")
		}
		result, err = c.UpgradeProvider(ctx, *name, *toVersion, env)
	case "retire":
		if *version < 1 || !env.Valid() || *reason == "" {
			return errors.New("providers retire: positive --version, --environment, and --reason are required")
		}
		result, err = c.RetireProvider(ctx, *name, *version, env, *reason)
	default:
		return fmt.Errorf("providers: unknown action %q", command)
	}
	if err != nil {
		return err
	}
	return printJSON(result)
}
