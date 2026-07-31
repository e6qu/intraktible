// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/e6qu/intraktible/client"
	"github.com/e6qu/intraktible/tenancy/domain"
)

type tenancyConnectionFlags struct {
	serverURL *string
	apiKey    *string
}

func bindTenancyConnection(fs *flag.FlagSet) tenancyConnectionFlags {
	return tenancyConnectionFlags{
		serverURL: fs.String("server", "http://localhost:8080", "intraktible server URL"),
		apiKey:    fs.String("api-key", os.Getenv("INTRAKTIBLE_API_KEY"), "API key"),
	}
}

func (flags tenancyConnectionFlags) client() *client.Client {
	return client.New(*flags.serverURL, *flags.apiKey)
}

func tenancyCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(
			"tenancy: command required (orgs|create-org|get-org|configure-org|suspend-org|" +
				"resume-org|delete-org|workspaces|create-workspace|get-workspace|configure-workspace|" +
				"suspend-workspace|resume-workspace|delete-workspace|memberships|grant-membership|" +
				"revoke-membership)",
		)
	}
	switch args[0] {
	case "orgs":
		return tenancyListOrgs(args[1:])
	case "create-org":
		return tenancyCreateOrg(args[1:])
	case "get-org":
		return tenancyGetOrg(args[1:])
	case "configure-org", "suspend-org", "resume-org", "delete-org":
		return tenancyOrgAction(args[0], args[1:])
	case "workspaces":
		return tenancyListOrgs2(args[1:])
	case "create-workspace":
		return tenancyCreateWorkspace(args[1:])
	case "get-workspace":
		return tenancyGetWorkspace(args[1:])
	case "configure-workspace", "suspend-workspace", "resume-workspace", "delete-workspace":
		return tenancyWorkspaceAction(args[0], args[1:])
	case "memberships":
		return tenancyListMemberships(args[1:])
	case "grant-membership":
		return tenancyGrantMembership(args[1:])
	case "revoke-membership":
		return tenancyRevokeMembership(args[1:])
	default:
		return fmt.Errorf("tenancy: unknown command %q", args[0])
	}
}

func tenancyListOrgs(args []string) error {
	fs := flag.NewFlagSet("tenancy orgs", flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListOrganizations(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func tenancyCreateOrg(args []string) error {
	fs := flag.NewFlagSet("tenancy create-org", flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	key := fs.String("key", "", "organization key (URL-safe slug)")
	display := fs.String("display", "", "organization display name")
	plan := fs.String("plan", "", "plan name")
	maxWorkspaces := fs.Int("max-workspaces", 0, "workspace quota (0 = unlimited)")
	adminActor := fs.String("admin-actor", "", "initial admin actor")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *key == "" || *display == "" || *adminActor == "" {
		return errors.New("tenancy create-org: --key, --display, and --admin-actor are required")
	}
	created, err := connection.client().CreateOrganization(context.Background(), client.TenancyOrgCreateRequest{
		Key: *key, Display: *display, AdminActor: *adminActor,
		Config: domain.OrganizationConfig{Plan: *plan, MaxWorkspaces: *maxWorkspaces},
	})
	if err != nil {
		return err
	}
	return printJSON(created)
}

func tenancyGetOrg(args []string) error {
	fs := flag.NewFlagSet("tenancy get-org", flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	org := fs.String("org", "", "organization key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *org == "" {
		return errors.New("tenancy get-org: --org is required")
	}
	item, err := connection.client().GetOrganization(context.Background(), *org)
	if err != nil {
		return err
	}
	return printJSON(item)
}

func tenancyOrgAction(command string, args []string) error {
	fs := flag.NewFlagSet("tenancy "+command, flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	org := fs.String("org", "", "organization key")
	reason := fs.String("reason", "", "reason (suspend/delete)")
	plan := fs.String("plan", "", "plan name (configure)")
	maxWorkspaces := fs.Int("max-workspaces", 0, "workspace quota (configure)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *org == "" {
		return fmt.Errorf("tenancy %s: --org is required", command)
	}
	c := connection.client()
	ctx := context.Background()
	var result any
	var err error
	switch command {
	case "configure-org":
		result, err = c.ConfigureOrganization(ctx, *org, domain.OrganizationConfig{
			Plan: *plan, MaxWorkspaces: *maxWorkspaces,
		})
	case "suspend-org":
		result, err = c.SuspendOrganization(ctx, *org, *reason)
	case "resume-org":
		result, err = c.ResumeOrganization(ctx, *org)
	case "delete-org":
		result, err = c.DeleteOrganization(ctx, *org, *reason)
	default:
		return fmt.Errorf("tenancy: unknown org action %q", command)
	}
	if err != nil {
		return err
	}
	return printJSON(result)
}

func tenancyListOrgs2(args []string) error {
	fs := flag.NewFlagSet("tenancy workspaces", flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	org := fs.String("org", "", "organization key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *org == "" {
		return errors.New("tenancy workspaces: --org is required")
	}
	items, err := connection.client().ListWorkspaces(context.Background(), *org)
	if err != nil {
		return err
	}
	return printJSON(items)
}

func tenancyCreateWorkspace(args []string) error {
	fs := flag.NewFlagSet("tenancy create-workspace", flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	org := fs.String("org", "", "organization key")
	key := fs.String("key", "", "workspace key")
	display := fs.String("display", "", "workspace display name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *org == "" || *key == "" || *display == "" {
		return errors.New("tenancy create-workspace: --org, --key, and --display are required")
	}
	result, err := connection.client().CreateWorkspace(
		context.Background(), *org, *key, *display, domain.WorkspaceConfig{},
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func tenancyGetWorkspace(args []string) error {
	return tenancyOrgScopedRead(args, "tenancy get-workspace",
		func(c *client.Client, org, workspace string) (any, error) {
			return c.GetWorkspace(context.Background(), org, workspace)
		})
}

// tenancyOrgScopedRead implements the shared "--org --workspace required, then one
// client read, then print" shape used by the workspace and membership reads.
func tenancyOrgScopedRead(
	args []string,
	command string,
	read func(c *client.Client, org, workspace string) (any, error),
) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	org := fs.String("org", "", "organization key")
	workspace := fs.String("workspace", "", "workspace key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *org == "" || *workspace == "" {
		return fmt.Errorf("%s: --org and --workspace are required", command)
	}
	item, err := read(connection.client(), *org, *workspace)
	if err != nil {
		return err
	}
	return printJSON(item)
}

func tenancyWorkspaceAction(command string, args []string) error {
	fs := flag.NewFlagSet("tenancy "+command, flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	org := fs.String("org", "", "organization key")
	workspace := fs.String("workspace", "", "workspace key")
	reason := fs.String("reason", "", "reason (suspend/delete)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *org == "" || *workspace == "" {
		return fmt.Errorf("tenancy %s: --org and --workspace are required", command)
	}
	c := connection.client()
	ctx := context.Background()
	var result any
	var err error
	switch command {
	case "configure-workspace":
		result, err = c.ConfigureWorkspace(ctx, *org, *workspace, domain.WorkspaceConfig{})
	case "suspend-workspace":
		result, err = c.SuspendWorkspace(ctx, *org, *workspace, *reason)
	case "resume-workspace":
		result, err = c.ResumeWorkspace(ctx, *org, *workspace)
	case "delete-workspace":
		result, err = c.DeleteWorkspace(ctx, *org, *workspace, *reason)
	default:
		return fmt.Errorf("tenancy: unknown workspace action %q", command)
	}
	if err != nil {
		return err
	}
	return printJSON(result)
}

func tenancyListMemberships(args []string) error {
	return tenancyOrgScopedRead(args, "tenancy memberships",
		func(c *client.Client, org, workspace string) (any, error) {
			return c.ListMemberships(context.Background(), org, workspace)
		})
}

func tenancyGrantMembership(args []string) error {
	fs := flag.NewFlagSet("tenancy grant-membership", flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	org := fs.String("org", "", "organization key")
	workspace := fs.String("workspace", "", "workspace key")
	actor := fs.String("actor", "", "actor identity")
	role := fs.String("role", "", "role (viewer|operator|editor|approver|admin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *org == "" || *workspace == "" || *actor == "" || *role == "" {
		return errors.New("tenancy grant-membership: --org, --workspace, --actor, and --role are required")
	}
	result, err := connection.client().GrantMembership(
		context.Background(), *org, *workspace, *actor, domain.MembershipRole(*role),
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func tenancyRevokeMembership(args []string) error {
	fs := flag.NewFlagSet("tenancy revoke-membership", flag.ContinueOnError)
	connection := bindTenancyConnection(fs)
	org := fs.String("org", "", "organization key")
	workspace := fs.String("workspace", "", "workspace key")
	actor := fs.String("actor", "", "actor identity")
	reason := fs.String("reason", "", "revocation reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *org == "" || *workspace == "" || *actor == "" || strings.TrimSpace(*reason) == "" {
		return errors.New(
			"tenancy revoke-membership: --org, --workspace, --actor, and --reason are required",
		)
	}
	result, err := connection.client().RevokeMembership(
		context.Background(), *org, *workspace, *actor, *reason,
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}
