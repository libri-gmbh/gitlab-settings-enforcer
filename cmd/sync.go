package cmd

import (
	gl "github.com/libri-gmbh/gitlab-settings-enforcer/pkg/gitlab"
	"github.com/spf13/cobra"
)

// syncCmd represents the sync command
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync gitlab's project settings with the config",
	Run: func(cmd *cobra.Command, args []string) {
		client, err := gitlabClient()
		if err != nil {
			logger.Fatal(err)
		}

		if env.Dryrun {
			logger.Infof("DRYRUN: No settings will be updated.")
		}

		manager := gl.NewProjectManager(
			logger.WithField("module", "project_manager"),
			client.Groups,
			client.Projects,
			client.ProtectedBranches,
			client.ProtectedTags,
			client.Branches,
			cfg,
		)

		projects, err := manager.GetProjects()
		if err != nil {
			logger.Fatal(err)
		}

		logger.Infof("Identified %d valid project(s).", len(projects))
		for index, project := range projects {
			logger.Infof("Processing project #%d: %s", index+1, project.PathWithNamespace)

			// Update branches
			if err := manager.EnsureBranchesAndProtection(project, env.Dryrun); err != nil {
				logger.Errorf("failed to ensure branches of repo %v: %v", project.PathWithNamespace, err)
				manager.SetError(true)
			}

			// Update general settings
			if err := manager.UpdateProjectSettings(project, env.Dryrun); err != nil {
				logger.Errorf("failed to update project settings of repo %v: %v", project.PathWithNamespace, err)
				manager.SetError(true)
			}

			// Update approval settings
			if err := manager.UpdateProjectApprovalSettings(project, env.Dryrun); err != nil {
				logger.Errorf("failed to update approval settings of repo %v: %v", project.PathWithNamespace, err)
				manager.SetError(true)
			}
		}

		if err := manager.GenerateChangeLogReport(); err != nil {
			logger.Errorf("failed to create changelog report: %v", err)
			manager.SetError(true)
		}

		if manager.GetError() {
			logger.Fatal("Error(s) encountered.")
		}
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// syncCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// syncCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
