// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/e6qu/intraktible/client"
	"github.com/e6qu/intraktible/packs/domain"
)

type packsConnectionFlags struct {
	serverURL *string
	apiKey    *string
}

func bindPacksConnection(fs *flag.FlagSet) packsConnectionFlags {
	return packsConnectionFlags{
		serverURL: fs.String("server", "http://localhost:8080", "intraktible server URL"),
		apiKey:    fs.String("api-key", os.Getenv("INTRAKTIBLE_API_KEY"), "API key"),
	}
}

func (flags packsConnectionFlags) client() *client.Client {
	return client.New(*flags.serverURL, *flags.apiKey)
}

func packsCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(
			"packs: command required (define|list|get|install|upgrade|rollback|retire)",
		)
	}
	switch args[0] {
	case "define":
		return packsDefine(args[1:])
	case "list":
		return packsList(args[1:])
	case "get":
		return packsGet(args[1:])
	case "install", "upgrade", "rollback", "retire":
		return packsAction(args[0], args[1:])
	default:
		return fmt.Errorf("packs: unknown command %q", args[0])
	}
}

func packsDefine(args []string) error {
	fs := flag.NewFlagSet("packs define", flag.ContinueOnError)
	connection := bindPacksConnection(fs)
	name := fs.String("name", "", "pack name (URL-safe slug)")
	title := fs.String("title", "", "pack title")
	description := fs.String("description", "", "pack description")
	signature := fs.String("signature", "", "pack signature")
	flowID := fs.String("flow-id", "", "bundled flow artifact id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *title == "" || *description == "" || *signature == "" || *flowID == "" {
		return errors.New("packs define: --name, --title, --description, --signature, and --flow-id are required")
	}
	accepted, err := connection.client().DefinePack(context.Background(), domain.Manifest{
		Name: *name, Title: *title, Description: *description, Signature: *signature,
		Artifacts: []domain.Artifact{
			{Kind: domain.ArtifactFlow, ID: *flowID, Content: map[string]any{"graph": "..."}},
		},
	})
	if err != nil {
		return err
	}
	return printJSON(accepted)
}

func packsList(args []string) error {
	fs := flag.NewFlagSet("packs list", flag.ContinueOnError)
	connection := bindPacksConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListPacks(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func packsGet(args []string) error {
	fs := flag.NewFlagSet("packs get", flag.ContinueOnError)
	connection := bindPacksConnection(fs)
	name := fs.String("name", "", "pack name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("packs get: --name is required")
	}
	item, err := connection.client().GetPack(context.Background(), *name)
	if err != nil {
		return err
	}
	return printJSON(item)
}

func packsAction(command string, args []string) error {
	fs := flag.NewFlagSet("packs "+command, flag.ContinueOnError)
	connection := bindPacksConnection(fs)
	name := fs.String("name", "", "pack name")
	version := fs.Int("version", 0, "pack version")
	reason := fs.String("reason", "", "reason (retire)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("packs %s: --name is required", command)
	}
	c := connection.client()
	ctx := context.Background()
	var result any
	var err error
	switch command {
	case "install":
		if *version < 1 {
			return errors.New("packs install: positive --version is required")
		}
		result, err = c.InstallPack(ctx, *name, *version)
	case "upgrade":
		if *version < 1 {
			return errors.New("packs upgrade: positive --version is required")
		}
		result, err = c.UpgradePack(ctx, *name, *version)
	case "rollback":
		if *version < 1 {
			return errors.New("packs rollback: positive --version is required")
		}
		result, err = c.RollbackPack(ctx, *name, *version)
	case "retire":
		if *reason == "" {
			return errors.New("packs retire: --reason is required")
		}
		result, err = c.RetirePack(ctx, *name, *reason)
	default:
		return fmt.Errorf("packs: unknown action %q", command)
	}
	if err != nil {
		return err
	}
	return printJSON(result)
}
