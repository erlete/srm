package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/erlete/srm/internal/service"
)

func newGroupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "List and create runner groups",
	}
	cmd.AddCommand(newGroupsListCmd(), newGroupsCreateCmd())
	return cmd
}

func newGroupsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List runner groups across all configured orgs (use --org to filter to one)",
		RunE: func(*cobra.Command, []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}

			var rows []service.GroupWithOrg
			if flagOrg != "" {
				gs, err := mgr.ListGroups(context.Background(), flagOrg)
				if err != nil {
					return err
				}
				for _, g := range gs {
					rows = append(rows, service.GroupWithOrg{Org: flagOrg, Group: g})
				}
			} else {
				var errs map[string]error
				rows, errs = mgr.ListAllGroups(context.Background())
				for _, org := range mgr.OrgNames() {
					if e := errs[org]; e != nil {
						fmt.Printf("WARN %s: %v\n", org, e)
					}
				}
			}

			fmt.Printf("%-16s %-6s %-24s %-11s %-8s %s\n", "ORG", "ID", "NAME", "VISIBILITY", "DEFAULT", "PUBLIC")
			for _, row := range rows {
				g := row.Group
				fmt.Printf("%-16s %-6d %-24s %-11s %-8v %v\n", row.Org, g.ID, g.Name, g.Visibility, g.Default, g.AllowsPublic)
			}
			return nil
		},
	}
}

func newGroupsCreateCmd() *cobra.Command {
	var visibility string
	c := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a runner group in an org (--org, or the sole org; honors --dry-run)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			mgr, closeLog, err := buildManager()
			if err != nil {
				return err
			}
			defer closeLog()
			if err := requireOrgs(mgr); err != nil {
				return err
			}
			org, err := targetOrg(mgr)
			if err != nil {
				return err
			}
			g, err := mgr.CreateGroup(context.Background(), org, args[0], visibility)
			if err != nil {
				return err
			}
			fmt.Printf("created group %q in %s (id %d, visibility %s)\n", g.Name, org, g.ID, g.Visibility)
			return nil
		},
	}
	c.Flags().StringVar(&visibility, "visibility", "all", "group visibility: all|selected|private")
	return c
}
