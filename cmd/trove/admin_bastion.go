package main

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/bastionmain"
)

// The Bastion commands (R-BAS-004, R-AUTH-015's pattern): register a Bastion, issue its token and revoke one.
// They call the same registration service as the management API, in the same transaction with the change
// record and the audit event, with the actor admin-cli.

const bastionUsage = "usage: trove admin bastion create <name> [--description <text>] [--config <file>]\n" +
	"       trove admin bastion token create <name> [--ttl 8760h] [--description <text>] [--config <file>]\n" +
	"       trove admin bastion token revoke <name> <token-id> [--config <file>]"

// bastionCmd is trove admin bastion create|token create|token revoke.
func bastionCmd(args []string, s streams) error {
	if len(args) == 0 {
		return usageError{bastionUsage}
	}
	switch args[0] {
	case "create":
		return bastionCreate(args[1:], s)
	case "token":
		if len(args) >= 2 {
			switch args[1] {
			case "create":
				return bastionTokenCreate(args[2:], s)
			case "revoke":
				return bastionTokenRevoke(args[2:], s)
			}
		}
	}
	return usageError{bastionUsage}
}

// withBastions opens the command environment and runs fn with the registration service.
func withBastions(c *commonFlags, s streams, args []string, fn func(context.Context, *identityParts, *bastionmain.Registrations) error) error {
	return withIdentity(c, s, args, func(ctx context.Context, id *identityParts) error {
		return fn(ctx, id, id.bastions)
	})
}

// bastionCreate is trove admin bastion create <name>.
func bastionCreate(args []string, s streams) error {
	name, rest := positional(args)
	c := newFlags("admin bastion create", s)
	description := c.fs.String("description", "", "description of the registration")
	if name == "" {
		return usageError{bastionUsage}
	}
	return withBastions(c, s, rest, func(ctx context.Context, id *identityParts, b *bastionmain.Registrations) error {
		reg, err := b.Create(ctx, id.caller, bastionmain.NewRegistration{Name: name, Description: *description, Source: "admin-cli"})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(s.stdout, "bastion %s registered (%s)\n", reg.Name, reg.ID)
		return err
	})
}

// bastionTokenCreate prints the secret of a new Bastion token to stdout, and nothing else, so that
// T=$(trove admin bastion token create ...) captures it. The secret is never logged.
func bastionTokenCreate(args []string, s streams) error {
	name, rest := positional(args)
	c := newFlags("admin bastion token create", s)
	ttl := c.fs.Duration("ttl", 0, "lifetime of the token; auth.tokens.bst.default_ttl when absent, at most auth.tokens.bst.max_ttl")
	description := c.fs.String("description", "", "description of the token")
	if name == "" {
		return usageError{bastionUsage}
	}
	return withBastions(c, s, rest, func(ctx context.Context, id *identityParts, b *bastionmain.Registrations) error {
		created, err := b.CreateToken(ctx, id.caller, name, bastionmain.TokenRequest{Description: *description, TTL: *ttl, Via: "admin-cli"})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(s.stdout, created.Secret)
		return err
	})
}

// bastionTokenRevoke is trove admin bastion token revoke <name> <token-id>.
func bastionTokenRevoke(args []string, s streams) error {
	name, rest := positional(args)
	tid, rest := positional(rest)
	c := newFlags("admin bastion token revoke", s)
	if name == "" || tid == "" {
		return usageError{bastionUsage}
	}
	return withBastions(c, s, rest, func(ctx context.Context, id *identityParts, b *bastionmain.Registrations) error {
		if err := b.RevokeToken(ctx, id.caller, name, tid); err != nil {
			return err
		}
		_, err := fmt.Fprintf(s.stdout, "token %s of bastion %s revoked\n", tid, name)
		return err
	})
}
