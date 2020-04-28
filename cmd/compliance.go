package cmd

import (
	"github.com/spf13/cobra"

	gl "github.com/libri-gmbh/gitlab-settings-enforcer/pkg/gitlab"
)

// complianceCmd represents the compliance command
var complianceCmd = &cobra.Command{
	Use:   "compliance",
	Short: "Compare gitlab's project settings with desired state",
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
			client.Branches,
			cfg,
		)

		if !manager.ComplianceReady() {
			logger.Fatal("No compliance configuration.")
		}

		projects, err := manager.GetProjects()
		if err != nil {
			logger.Fatal(err)
		}

		logger.Infof("Identified %d valid project(s).", len(projects))
		for index, project := range projects {
			logger.Infof("Processing project #%d: %s", index+1, project.PathWithNamespace)

			// Get current approval settings
			approvalSettings, err := manager.GetProjectApprovalSettings(project)
			if err != nil {
				logger.Errorf("failed to get current project settings of project %s: %v", project.PathWithNamespace, err)
				manager.SetError(true)
			}

			// Record current approval settings
			manager.ApprovalSettingsOriginal[project.PathWithNamespace] = approvalSettings

			// Get current settings states
			projectSettings, err := manager.GetProjectSettings(project)
			if err != nil {
				logger.Errorf("failed to get current project settings of project %s: %v", project.PathWithNamespace, err)
				manager.SetError(true)
			}

			// Record current settings states
			manager.ProjectSettingsOriginal[project.PathWithNamespace] = projectSettings
		}

		if err := manager.GenerateComplianceReport(); err != nil {
			logger.Errorf("failed to create changelog report: %v", err)
			manager.SetError(true)
		}

		if err := manager.GenerateComplianceEmail(); err != nil {
			logger.Errorf("failed to email changelog report: %v", err)
			manager.SetError(true)
		}

		if manager.GetError() {
			logger.Fatal("Error(s) encountered.")
		}
	},
}

func init() {
	rootCmd.AddCommand(complianceCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// complianceCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// complianceCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
