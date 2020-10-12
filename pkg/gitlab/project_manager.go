package gitlab

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/r3labs/diff"
	"github.com/sirupsen/logrus"
	"github.com/xanzy/go-gitlab"

	"github.com/libri-gmbh/gitlab-settings-enforcer/pkg/config"
	"github.com/libri-gmbh/gitlab-settings-enforcer/pkg/internal/stringslice"
)

// ProjectManager fetches a list of repositories from GitLab
type ProjectManager struct {
	logger                   *logrus.Entry
	groupsClient             groupsClient
	projectsClient           projectsClient
	protectedBranchesClient  protectedBranchesClient
	protectedTagsClient      protectedTagsClient
	branchesClient           branchesClient
	config                   *config.Config
	ApprovalSettingsOriginal map[string]*gitlab.ProjectApprovals
	ApprovalSettingsUpdated  map[string]*gitlab.ProjectApprovals
	ProjectSettingsOriginal  map[string]*gitlab.Project
	ProjectSettingsUpdated   map[string]*gitlab.Project
}

// NewProjectManager returns a new ProjectManager instance
func NewProjectManager(
	logger *logrus.Entry,
	groupsClient groupsClient,
	projectsClient projectsClient,
	protectedBranchesClient protectedBranchesClient,
	protectedTagsClient protectedTagsClient,
	branchesClient branchesClient,
	config *config.Config,
) *ProjectManager {
	return &ProjectManager{
		logger:                   logger,
		groupsClient:             groupsClient,
		projectsClient:           projectsClient,
		protectedBranchesClient:  protectedBranchesClient,
		protectedTagsClient:      protectedTagsClient,
		branchesClient:           branchesClient,
		config:                   config,
		ApprovalSettingsOriginal: make(map[string]*gitlab.ProjectApprovals),
		ApprovalSettingsUpdated:  make(map[string]*gitlab.ProjectApprovals),
		ProjectSettingsOriginal:  make(map[string]*gitlab.Project),
		ProjectSettingsUpdated:   make(map[string]*gitlab.Project),
	}
}

/**********************
 * Exported Functions *
 **********************/

// ComplianceReady determines if a compliance configuration is present
func (m *ProjectManager) ComplianceReady() bool {
	m.logger.Debugf("---[ Config ]---")
	m.logger.Debugf("%+v", m.config)

	if m.config.Compliance == nil {
		m.logger.Debugf("Compliance field of Config not configured.")
		return false
	}

	return true
}

// EnsureBranchesAndProtection ensures that
//  1) the default branch exists
//  2) all of the protected branches are configured correctly
func (m *ProjectManager) EnsureBranchesAndProtection(project gitlab.Project, dryrun bool) error {
	if err := m.ensureDefaultBranch(project, dryrun); err != nil {
		return err
	}

	for _, b := range m.config.ProtectedBranches {
		protectedBranch, _, err := m.protectedBranchesClient.GetProtectedBranch(project.ID, b.Name)
		if err != nil {
			m.logger.Warnf("failed to get protected branch %v: %v", b.Name, err)
		} else {
			if protectedBranch != nil &&
				compareAccessLevels(protectedBranch.MergeAccessLevels, b.MergeAccessLevel) &&
				compareAccessLevels(protectedBranch.PushAccessLevels, b.PushAccessLevel) {
				continue
			}
		}

		if dryrun {
			m.logger.Infof("DRYRUN: Skipped executing API call [UnprotectRepositoryBranches] on %v branch.", b.Name)
			m.logger.Infof("DRYRUN: Skipped executing API call [ProtectRepositoryBranches] on %v branch.", b.Name)
			continue
		}

		// Remove protections (if present)
		if resp, err := m.protectedBranchesClient.UnprotectRepositoryBranches(project.ID, b.Name); err != nil &&
			(resp == nil || resp.StatusCode != http.StatusNotFound) {
			return fmt.Errorf("failed to unprotect branch %v before protection: %v", b.Name, err)
		}

		opt := &gitlab.ProtectRepositoryBranchesOptions{
			Name:             gitlab.String(b.Name),
			PushAccessLevel:  b.PushAccessLevel.Value(),
			MergeAccessLevel: b.MergeAccessLevel.Value(),
		}

		// (Re)add protections
		if _, _, err := m.protectedBranchesClient.ProtectRepositoryBranches(project.ID, opt); err != nil {
			return fmt.Errorf("failed to protect branch %s: %v", b.Name, err)
		}
	}

	return nil
}

func compareAccessLevels(branchLevel []*gitlab.BranchAccessDescription, configLevel config.AccessLevel) bool {
	return len(branchLevel) == 1 && branchLevel[0].AccessLevel == *configLevel.Value()
}

func (m *ProjectManager) EnsureTagsProtection(project gitlab.Project, dryrun bool) error {
	for _, t := range m.config.ProtectedTags {
		protectedTag, _, err := m.protectedTagsClient.GetProtectedTag(project.ID, t.Name)
		if err != nil {
			m.logger.Warnf("failed to get protected tag %v: %v", t.Name, err)
		} else {
			if protectedTag != nil &&
				len(protectedTag.CreateAccessLevels) == 1 && protectedTag.CreateAccessLevels[0].AccessLevel == *t.CreateAccessLevel.Value() {
				continue
			}
		}

		if dryrun {
			m.logger.Infof("DRYRUN: Skipped executing API call [UnprotectRepositoryTags] on %v tag.", t.Name)
			m.logger.Infof("DRYRUN: Skipped executing API call [ProtectRepositoryTags] on %v tag.", t.Name)
			continue
		}

		// Remove protections (if present)
		if resp, err := m.protectedTagsClient.UnprotectRepositoryTags(project.ID, t.Name); err != nil &&
			(resp == nil || resp.StatusCode != http.StatusNotFound) {
			return fmt.Errorf("failed to unprotect branch %v before protection: %v", t.Name, err)
		}

		opt := &gitlab.ProtectRepositoryTagsOptions{
			Name:              gitlab.String(t.Name),
			CreateAccessLevel: t.CreateAccessLevel.Value(),
		}

		// (Re)add protections
		if _, _, err := m.protectedTagsClient.ProtectRepositoryTags(project.ID, opt); err != nil {
			return fmt.Errorf("failed to protect branch %s: %v", t.Name, err)
		}
	}

	return nil
}

// GetError returns the Error status
func (m *ProjectManager) GetError() bool {
	return m.config.Error
}

// GenerateChangeLogReport to console the altered project settings
func (m *ProjectManager) GenerateChangeLogReport() error {
	m.logger.Debugf("Generate Change Log Report")

	if err := m.debugPrintAllSettings(); err != nil {
		panic(err)
	}

	// Get differences
	approvalDifflog, err := diff.Diff(m.ApprovalSettingsOriginal, m.ApprovalSettingsUpdated)
	if err != nil {
		panic(err)
	}
	projectDifflog, err := diff.Diff(m.ProjectSettingsOriginal, m.ProjectSettingsUpdated)
	if err != nil {
		panic(err)
	}

	m.logger.Debugf("---[ Approval Diff Log ]---")
	m.logger.Debugf("%+v\n", approvalDifflog)
	m.logger.Debugf("---[ Project Diff Log ]---")
	m.logger.Debugf("%+v\n", projectDifflog)

	changelog := make(map[string]map[string]map[string]map[string]interface{})

	// Process Approvals
	m.logger.Debugf("Process Approval Diff Log")
	for _, v := range approvalDifflog {
		// If REPO doesn't exist in map, make it.
		if _, ok := changelog[v.Path[0]]; !ok {
			changelog[v.Path[0]] = make(map[string]map[string]map[string]interface{})
		}
		if _, ok := changelog[v.Path[0]]["approval_settings"]; !ok {
			changelog[v.Path[0]]["approval_settings"] = make(map[string]map[string]interface{})
		}

		setting_name := strcase.ToSnake(v.Path[len(v.Path)-1])
		changelog[v.Path[0]]["approval_settings"][setting_name] = make(map[string]interface{})
		changelog[v.Path[0]]["approval_settings"][setting_name]["From"] = v.From
		changelog[v.Path[0]]["approval_settings"][setting_name]["To"] = v.To
	}

	// Process Projects
	m.logger.Debugf("Process Project Diff Log")
	for _, v := range projectDifflog {
		// If REPO doesn't exist in map, make it.
		if _, ok := changelog[v.Path[0]]; !ok {
			changelog[v.Path[0]] = make(map[string]map[string]map[string]interface{})
		}
		if _, ok := changelog[v.Path[0]]["project_settings"]; !ok {
			changelog[v.Path[0]]["project_settings"] = make(map[string]map[string]interface{})
		}

		setting_name := strcase.ToSnake(v.Path[len(v.Path)-1])
		changelog[v.Path[0]]["project_settings"][setting_name] = make(map[string]interface{})
		changelog[v.Path[0]]["project_settings"][setting_name]["From"] = v.From
		changelog[v.Path[0]]["project_settings"][setting_name]["To"] = v.To
	}

	// Output Raw JSON
	body, err := json.MarshalIndent(changelog, "", "  ")
	if err != nil {
		panic(err)
	}
	m.logger.Debugf("---[ Change Log (JSON) ]---")
	m.logger.Debugf("%s\n", string(body))

	if len(changelog) != 0 {
		// Get longest length of setting name
		var longest_setting_name int
		var project_names []string
		for project_name, subsections := range changelog {
			// Add to list of project names to allow sorting
			project_names = append(project_names, project_name)

			for _, data := range subsections {
				for setting := range data {
					if len(setting) > longest_setting_name {
						longest_setting_name = len(setting)
					}
				}
			}
		}
		sort.Strings(project_names)

		// Output Formated Report
		fmt.Printf("\nCHANGE LOG\n")

		for _, name := range project_names {
			fmt.Printf("  %s\n", name)

			var subsections []string
			for subsection := range changelog[name] {
				subsections = append(subsections, subsection)
			}
			sort.Strings(subsections)

			for _, subsection := range subsections {
				var settings []string
				for setting := range changelog[name][subsection] {
					settings = append(settings, setting)
				}
				sort.Strings(settings)

				for _, setting := range settings {
					fmt.Printf("    %-*s", longest_setting_name+2, setting+":")
					fmt.Printf("\"%v\" => \"%v\"\n", changelog[name][subsection][setting]["From"], changelog[name][subsection][setting]["To"])
				}
			}

			fmt.Printf("\n")
		}
	} else {
		fmt.Printf("\nNo changes discovered.\n")
	}

	return nil
}

// GenerateComplianceEmail emails the compliance state of mandatory settings
func (m *ProjectManager) GenerateComplianceEmail() error {
	if m.config.Compliance.Email.From == "" || m.config.Compliance.Email.Server == "" || m.config.Compliance.Email.Port == 0 {
		m.logger.Debugf("---[ Skipping Compliance Settings as From, Server or Port is not set ]---")
		return nil
	}

	if err := m.debugPrintAllSettings(); err != nil {
		panic(err)
	}

	m.logger.Debugf("---[ Compliance Settings ]---")
	m.logger.Debugf("%v\n", m.config.Compliance)

	// Create sorted list of projects
	var project_names []string
	for project_name := range m.ProjectSettingsOriginal {
		// Add to list of project names to allow sorting
		project_names = append(project_names, project_name)

	}
	sort.Strings(project_names)

	// Create sorted list of subsections
	var subsections []string
	for subsection := range m.config.Compliance.Mandatory {
		subsections = append(subsections, subsection)
	}
	sort.Strings(subsections)

	// Create sorted list of settings, per subsection
	var longestSettingName int
	var settings = make(map[string][]string)
	for _, subsection := range subsections {
		settings[subsection] = make([]string, 0)

		for setting := range m.config.Compliance.Mandatory[subsection] {
			settings[subsection] = append(settings[subsection], setting)
			if len(setting) > longestSettingName {
				longestSettingName = len(setting)
			}
		}
		sort.Strings(settings[subsection])
	}

	// Print Title
	emailBody := "\r\n<h2>Compliance Report</h2>\r\n"
	emailBody += "<table>\r\n"

	// Loop through projects
	for _, name := range project_names {
		emailBody += " <tr>\r\n"
		emailBody += fmt.Sprintf("  <td colspan=\"2\" style=\"text-indent:20px\"><b>%s</b></td>\r\n", name)
		emailBody += " </tr>\r\n"

		// Loop through subsections
		for _, subsection := range subsections {
			emailBody += " <tr>\r\n"
			emailBody += fmt.Sprintf("  <td colspan=\"2\" style=\"text-indent:40px\"><b>%s</b></td>\r\n", subsection)
			emailBody += " </tr>\r\n"

			// Loop through settings
			for _, setting := range settings[subsection] {
				emailBody += " <tr>\r\n"
				emailBody += fmt.Sprintf("  <td style=\"text-indent:60px\">%-*s</td>", longestSettingName+2, setting+":")

				var settingValue interface{}
				switch subsection {
				case "approval_settings":
					structure := reflect.ValueOf(m.ApprovalSettingsOriginal[name])
					field := structure.Elem().FieldByName(strcase.ToCamel(setting))
					if field.IsValid() {
						settingValue = field.Interface()
					} else {
						settingValue = "NOT VALID SETTING"
					}
				case "project_settings":
					structure := reflect.ValueOf(m.ProjectSettingsOriginal[name])
					field := structure.Elem().FieldByName(strcase.ToCamel(setting))
					if field.IsValid() {
						settingValue = field.Interface()
					} else {
						settingValue = "NOT VALID SETTING"
					}
				}

				emailBody += fmt.Sprintf("  <td style=\"text-indent:40px\">%v", settingValue)

				if settingValue != m.config.Compliance.Mandatory[subsection][setting] {
					emailBody += fmt.Sprintf(" (%v)", m.config.Compliance.Mandatory[subsection][setting])
				}

				emailBody += "</td>\r\n"
				emailBody += "</tr>\r\n"
			}
		}

		emailBody += "</table>\r\n"
	}

	if err := m.SendEmail(m.config.Compliance.Email.To, m.config.Compliance.Email.From, "Compliance Report", emailBody); err != nil {
		m.logger.Fatal(err)
	}

	return nil
}

// GenerateComplianceReport prints to console the compliance state of mandatory settings
func (m *ProjectManager) GenerateComplianceReport() error {
	if err := m.debugPrintAllSettings(); err != nil {
		panic(err)
	}

	m.logger.Debugf("---[ Compliance Settings ]---")
	m.logger.Debugf("%v\n", m.config.Compliance)

	// Print Title
	fmt.Printf("\nCOMPLIANCE REPORT\n")

	// Create sorted list of projects
	var projectNames []string
	for projectName := range m.ProjectSettingsOriginal {
		// Add to list of project names to allow sorting
		projectNames = append(projectNames, projectName)

	}
	sort.Strings(projectNames)

	// Create sorted list of subsections
	var subsections []string
	for subsection := range m.config.Compliance.Mandatory {
		subsections = append(subsections, subsection)
	}
	sort.Strings(subsections)

	// Create sorted list of settings, per subsection
	var longestSettingName int
	var settings = make(map[string][]string)
	for _, subsection := range subsections {
		settings[subsection] = make([]string, 0)

		for setting := range m.config.Compliance.Mandatory[subsection] {
			settings[subsection] = append(settings[subsection], setting)
			if len(setting) > longestSettingName {
				longestSettingName = len(setting)
			}
		}
		sort.Strings(settings[subsection])
	}

	// Loop through projects
	for _, name := range projectNames {
		fmt.Printf("  %s\n", name)

		// Loop through subsections
		for _, subsection := range subsections {
			fmt.Printf("    %s:\n", subsection)

			// Loop through settings
			for _, setting := range settings[subsection] {
				fmt.Printf("      %-*s", longestSettingName+2, setting+":")

				var settingValue interface{}
				switch subsection {
				case "approval_settings":
					structure := reflect.ValueOf(m.ApprovalSettingsOriginal[name])
					field := structure.Elem().FieldByName(strcase.ToCamel(setting))
					if field.IsValid() {
						settingValue = field.Interface()
					} else {
						settingValue = "NOT VALID SETTING"
					}
				case "project_settings":
					structure := reflect.ValueOf(m.ProjectSettingsOriginal[name])
					field := structure.Elem().FieldByName(strcase.ToCamel(setting))
					if field.IsValid() {
						settingValue = field.Interface()
					} else {
						settingValue = "NOT VALID SETTING"
					}
				}

				fmt.Printf("%v", settingValue)

				if settingValue != m.config.Compliance.Mandatory[subsection][setting] {
					fmt.Printf(" (%v)", m.config.Compliance.Mandatory[subsection][setting])
				}

				fmt.Printf("\n")
			}
		}

		fmt.Printf("\n")
	}

	return nil
}

// GetProjectMergeRequestSettings identifies the current state of a GitLab projece
func (m *ProjectManager) GetProjectApprovalSettings(project gitlab.Project) (*gitlab.ProjectApprovals, error) {
	m.logger.Debugf("Get merge request approval settings of project %s ...", project.PathWithNamespace)

	returnedApproval, response, err := m.projectsClient.GetApprovalConfiguration(project.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get current approval settings of project %s: %v", project.PathWithNamespace, err)
	}

	m.logger.Debugf("---[ HTTP Response for GetProjectApprovalSettings ]---\n")
	m.logger.Debugf("%v\n", response)
	m.logger.Debugf("---[ Returned MR for GetProjectApprovalSettings ]---\n")
	m.logger.Debugf("%v\n", returnedApproval)

	return returnedApproval, nil
}

// GetProjects fetches a list of accessible repos within the groups set in config file
func (m *ProjectManager) GetProjects() ([]gitlab.Project, error) {
	var repos []gitlab.Project

	m.logger.Debugf("Fetching projects under %s path ...", m.config.GroupName)

	// Identify Group/Subgroup's ID
	var groupID int

	m.logger.Debugf("Identifying %s's GroupID", m.config.GroupName)
	if strings.ContainsAny(m.config.GroupName, "/") {
		// Nested Path
		group_ID, err := m.GetSubgroupID(m.config.GroupName, 1, 0)
		if err != nil {
			return []gitlab.Project{}, fmt.Errorf("failed to fetch GitLab group info for %q: %v", m.config.GroupName, err)
		}
		groupID = group_ID
	} else {
		// BugFix: Without this pre-processing, go-gitlab library stalls.
		var groupName = strings.Replace(url.PathEscape(m.config.GroupName), ".", "%2E", -1)
		group, _, err := m.groupsClient.GetGroup(groupName)
		if err != nil {
			return []gitlab.Project{}, fmt.Errorf("failed to fetch GitLab group info for %q: %v", groupName, err)
		}
		groupID = group.ID
	}

	m.logger.Debugf("GroupID is %d", groupID)

	// Get Project objects
	for {
		projects, resp, err := m.groupsClient.ListGroupProjects(groupID, listGroupProjectOps)
		if err != nil {
			return []gitlab.Project{}, fmt.Errorf("failed to fetch GitLab projects for %s [%d]: %v", m.config.GroupName, groupID, err)
		}

		for _, p := range projects {
			if len(m.config.ProjectWhitelist) > 0 && !stringslice.Contains(p.PathWithNamespace, m.config.ProjectWhitelist) {
				m.logger.Debugf("Skipping repo %s as it's not whitelisted", p.PathWithNamespace)
				continue
			}
			if stringslice.Contains(p.PathWithNamespace, m.config.ProjectBlacklist) {
				m.logger.Debugf("Skipping repo %s as it's blacklisted", p.PathWithNamespace)
				continue
			}

			repos = append(repos, *p)
		}

		// Exit the loop when we've seen all pages.
		if listGroupProjectOps.Page >= resp.TotalPages || resp.TotalPages == 1 {
			break
		}

		// Update the page number to get the next page.
		listGroupProjectOps.Page = resp.NextPage
	}

	m.logger.Debugf("Fetching projects under path done. Retrieved %d.", len(repos))

	return repos, nil
}

// GetProjectSettings gets the settings in GitLab for the provided project, using
// the Project API
// https://docs.gitlab.com/ee/api/projects.html
func (m *ProjectManager) GetProjectSettings(project gitlab.Project) (*gitlab.Project, error) {
	m.logger.Debugf("Get project settings of project %s ...", project.PathWithNamespace)

	returnedProject, response, err := m.projectsClient.GetProject(project.ID, &gitlab.GetProjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get current project settings of project %s: %v", project.PathWithNamespace, err)
	}

	m.logger.Debugf("---[ HTTP Response ]---\n")
	m.logger.Debugf("%v\n", response)
	m.logger.Debugf("---[ Returned Project ]---\n")
	m.logger.Debugf("%v\n", returnedProject)

	return returnedProject, nil
}

// GetSubgroupID walks the provided path, returning the Group ID of the last desired subgroup.
func (m *ProjectManager) GetSubgroupID(path string, indent int, group_ID int) (int, error) {
	var subgroup_ID int

	subpath := strings.Split(path, "/")[indent]
	pathCount := len(strings.Split(path, "/")) - 1
	m.logger.Debugf("Walking %s, looking for %s[%d/%d].", path, subpath, indent, pathCount)

	var group_info string
	if group_ID == 0 {
		// Use base of path to get first group ID
		group_info = strings.Split(path, "/")[0]
	} else {
		// Use parent ID provided.
		group_info = strconv.Itoa(group_ID)
	}

	m.logger.Debugf("Getting Subgroup(s) of %v.", group_info)
	subgroups, _, err := m.groupsClient.ListSubgroups(group_info, listSubgroupOps)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch GitLab subgroups for %s [%s]: %v", path, subpath, err)
	}

	// Get desired subgroup_ID
	m.logger.Debugf("---[ Subgroup(s) Found: %d ]---\n", len(subgroups))
	for _, g := range subgroups {
		m.logger.Debugf(">>> %s <<<: %+v\n", g.Name, g)
		matched, _ := regexp.MatchString("^"+subpath+"$", g.Path)
		if matched {
			subgroup_ID = g.ID
		}
	}

	if indent != pathCount {
		m.logger.Debugf("Found Group ID %d, going deeper.", subgroup_ID)
		subgroup_ID, _ = m.GetSubgroupID(path, indent+1, subgroup_ID)
	}

	m.logger.Debugf("Coming back up from %s.", subpath)
	return subgroup_ID, nil
}

// SetError returns the Error status
func (m *ProjectManager) SetError(state bool) bool {
	m.config.Error = state
	return m.config.Error
}

// SendEmail
func (m *ProjectManager) SendEmail(to []string, from string, subject string, body string) error {
	// Connect to remote SMTP server
	smtpServer, err := smtp.Dial(m.config.Compliance.Email.Server + ":" + strconv.Itoa(m.config.Compliance.Email.Port))
	if err != nil {
		m.logger.Fatal(err)
	}

	// Set the sender
	if err := smtpServer.Mail(from); err != nil {
		m.logger.Fatal(err)
	}

	// Set the recipient
	if err := smtpServer.Rcpt(strings.Join(to, ",")); err != nil {
		m.logger.Fatal(err)
	}

	// Send the email body
	smtp_writer, err := smtpServer.Data()
	if err != nil {
		m.logger.Fatal(err)
	}

	message := "Content-Type: text/html; charset=UTF-8\r\n"
	message += fmt.Sprintf("From: %s\r\n", from)
	message += fmt.Sprintf("To: %s\r\n", strings.Join(to, ","))
	message += fmt.Sprintf("Subject: %s\r\n", subject)
	message += "<html>\r\n"
	message += " <head>\r\n"
	message += fmt.Sprintf("  <title>%s</title>\r\n", subject)
	message += " </head>\r\n"
	message += " <body>\r\n"
	message += fmt.Sprintf("\r\n%s\r\n", body)
	message += " </body>\r\n"
	message += "</html>"

	_, err = smtp_writer.Write([]byte(message))
	if err != nil {
		m.logger.Fatal(err)
	}

	err = smtp_writer.Close()
	if err != nil {
		m.logger.Fatal(err)
	}

	// Send the QUIT command and close the connection.
	err = smtpServer.Quit()
	if err != nil {
		m.logger.Fatal(err)
	}

	return nil
}

// UpdateProjectMergeRequestSettings updates the project settings on gitlab
func (m *ProjectManager) UpdateProjectApprovalSettings(project gitlab.Project, dryrun bool) error {
	m.logger.Debugf("Updating merge request approval settings of project %s [%d]...", project.PathWithNamespace, project.ID)

	// Exit if nothing to configure
	if m.config.ApprovalSettings == nil {
		m.logger.Debugf("No approval_settings section provided in config")
		return nil
	}

	// Get current settings states
	approvalSettings, err := m.GetProjectApprovalSettings(project)
	if err != nil {
		return fmt.Errorf("failed to get current project settings of project %s: %v", project.PathWithNamespace, err)
	}

	// Record current settings states
	m.ApprovalSettingsOriginal[project.PathWithNamespace] = approvalSettings

	m.logger.Debugf("---[ HTTP Payload for UpdateProjectApprovalSettings ]---\n")
	m.logger.Debugf("%+v\n", m.config.ApprovalSettings)

	settingsToChange, err := m.convertChangeApprovalConfigurationOptionsToProjectApprovals(*m.config.ApprovalSettings)
	if err != nil {
		return err
	}

	if !m.willChangeApprovalSettings(approvalSettings, &settingsToChange) {
		m.logger.Debugf("No action required.")

		// Record current settings states
		m.ApprovalSettingsUpdated[project.PathWithNamespace] = approvalSettings

		return nil
	}

	var returned_mr *gitlab.ProjectApprovals
	var response *gitlab.Response
	if dryrun {
		m.logger.Infof("DRYRUN: Skipped executing API call [ChangeApprovalConfiguration]")
	} else {
		returned_mr, response, err = m.projectsClient.ChangeApprovalConfiguration(project.ID, m.config.ApprovalSettings)
	}

	m.logger.Debugf("---[ HTTP Response for UpdateProjectApprovalSettings ]---\n")
	m.logger.Debugf("%v\n", response)
	m.logger.Debugf("---[ Returned MR for UpdateProjectApprovalSettings ]---\n")
	m.logger.Debugf("%v\n", returned_mr)

	if err != nil {
		return fmt.Errorf("failed to update merge request approval settings or project %s: %v", project.PathWithNamespace, err)
	}

	// Get new settings states
	approvalSettings, err = m.GetProjectApprovalSettings(project)
	if err != nil {
		return fmt.Errorf("failed to get current project settings of project %s: %v", project.PathWithNamespace, err)
	}

	// Record current settings states
	m.ApprovalSettingsUpdated[project.PathWithNamespace] = approvalSettings

	m.logger.Debugf("Updating merge request approval settings of project %s done.", project.PathWithNamespace)

	return nil
}

// UpdateProjectSettings updates the settings in GitLab for the provided project,
// using the Project API
// https://docs.gitlab.com/ee/api/projects.html
func (m *ProjectManager) UpdateProjectSettings(project gitlab.Project, dryrun bool) error {
	m.logger.Debugf("Updating project settings of project %s ...", project.PathWithNamespace)

	// Exit if nothing to configure.
	if m.config.ProjectSettings == nil {
		m.logger.Debugf("No project_settings section provided in config")
		return nil
	}

	// Get current settings states
	projectSettings, err := m.GetProjectSettings(project)
	if err != nil {
		return fmt.Errorf("failed to get current project settings of project %s: %v", project.PathWithNamespace, err)
	}

	// Record current settings states
	m.ProjectSettingsOriginal[project.PathWithNamespace] = projectSettings

	m.logger.Debugf("---[ HTTP Payload for UpdateProjectSettings ]---\n")
	m.logger.Debugf("%+v\n", m.config.ProjectSettings)

	settingsToChange, err := m.convertEditProjectOptionsToProject(*m.config.ProjectSettings)
	if err != nil {
		return err
	}

	if !m.willChangeProjectSettings(projectSettings, &settingsToChange) {
		m.logger.Debugf("No action required.")

		// Record current settings states
		m.ProjectSettingsUpdated[project.PathWithNamespace] = projectSettings

		return nil
	}

	var returned_project *gitlab.Project
	var response *gitlab.Response
	if dryrun {
		m.logger.Infof("DRYRUN: Skipped executing API call [EditProject]")
	} else {
		returned_project, response, err = m.projectsClient.EditProject(project.ID, m.config.ProjectSettings)
	}

	m.logger.Debugf("---[ HTTP Response for UpdateProjectSettings ]---\n")
	m.logger.Debugf("%v\n", response)
	m.logger.Debugf("---[ Returned Project for UpdateProjectSettings ]---\n")
	m.logger.Debugf("%v\n", returned_project)

	if err != nil {
		return fmt.Errorf("failed to update project settings of project %s: %v", project.PathWithNamespace, err)
	}

	// Get new settings states
	projectSettings, err = m.GetProjectSettings(project)
	if err != nil {
		return fmt.Errorf("failed to get current project settings of project %s: %v", project.PathWithNamespace, err)
	}

	// Record current settings states
	m.ProjectSettingsUpdated[project.PathWithNamespace] = projectSettings

	m.logger.Debugf("Updating project settings of project %s done.", project.PathWithNamespace)

	return nil
}

/**********************
 * Internal Functions *
 **********************/

// convertChangeApprovalConfigurationOptionsToProjectApprovals
func (m *ProjectManager) convertChangeApprovalConfigurationOptionsToProjectApprovals(current gitlab.ChangeApprovalConfigurationOptions) (gitlab.
	ProjectApprovals, error) {
	jsonData, err := json.Marshal(current)
	if err != nil {
		return gitlab.ProjectApprovals{}, fmt.Errorf("failed to convert ChangeApprovalConfigurationOptions to json: %v", err)
	}

	var returnValue gitlab.ProjectApprovals
	err = json.Unmarshal(jsonData, &returnValue)
	if err != nil {
		return gitlab.ProjectApprovals{}, fmt.Errorf("failed to convert json to ProjectApproval struct: %v", err)
	}

	return returnValue, nil
}

// convertEditProjectOptionsToProject
func (m *ProjectManager) convertEditProjectOptionsToProject(current gitlab.EditProjectOptions) (gitlab.Project, error) {
	jsonData, err := json.Marshal(current)
	if err != nil {
		return gitlab.Project{}, fmt.Errorf("failed to convert ChangeApprovalConfigurationOptions to json: %v", err)
	}

	var returnValue gitlab.Project
	err = json.Unmarshal(jsonData, &returnValue)
	if err != nil {
		return gitlab.Project{}, fmt.Errorf("failed to convert json to Project struct: %v", err)
	}

	return returnValue, nil
}

// debugPrintAllSettings prints to console all capture settings
func (m *ProjectManager) debugPrintAllSettings() error {
	m.logger.Debugf("---[ ORIGINAL APPROVAL SETTINGS ]---")
	if err := m.debugPrintApprovalSettings(m.ApprovalSettingsOriginal); err != nil {
		m.logger.Debugf("Error printing Original Approval Settings")
	}
	m.logger.Debugf("---[ UPDATED APPROVAL SETTINGS ]---")
	if err := m.debugPrintApprovalSettings(m.ApprovalSettingsUpdated); err != nil {
		m.logger.Debugf("Error printing Updated Approval Settings")
	}
	m.logger.Debugf("---[ ORIGINAL PROJECT SETTINGS ]---")
	if err := m.debugPrintProjectSettings(m.ProjectSettingsOriginal); err != nil {
		m.logger.Debugf("Error printing Original Project Settings")
	}
	m.logger.Debugf("---[ UPDATED PROJECT SETTINGS ]---")
	if err := m.debugPrintProjectSettings(m.ProjectSettingsUpdated); err != nil {
		m.logger.Debugf("Error printing Updated Project Settings")
	}

	return nil
}

// debugPrintProjectSettings prints to console a SettingsMap
func (m *ProjectManager) debugPrintApprovalSettings(settings map[string]*gitlab.ProjectApprovals) error {
	jsonData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to convert SettingsMap to JSON: %v", err)
	}

	m.logger.Debugf(string(jsonData))

	return nil
}

// debugPrintProjectSettings prints to console a SettingsMap
func (m *ProjectManager) debugPrintProjectSettings(settings map[string]*gitlab.Project) error {
	jsonData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to convert SettingsMap to JSON: %v", err)
	}

	m.logger.Debugf(string(jsonData))

	return nil
}

func (m *ProjectManager) ensureDefaultBranch(project gitlab.Project, dryrun bool) error {

	if !m.config.CreateDefaultBranch ||
		m.config.ProjectSettings.DefaultBranch == nil ||
		*m.config.ProjectSettings.DefaultBranch == "master" {
		return nil
	}

	opt := &gitlab.CreateBranchOptions{
		Branch: m.config.ProjectSettings.DefaultBranch,
		Ref:    gitlab.String("master"),
	}

	m.logger.Debugf("Ensuring default branch %s existence ... ", *opt.Branch)

	_, resp, err := m.branchesClient.GetBranch(project.ID, *opt.Branch)
	if err == nil {
		m.logger.Debugf("Ensuring default branch %s existence ... already exists!", *opt.Branch)
		return nil
	}

	if resp == nil {
		return fmt.Errorf("failed to check for default branch existence, got nil response")
	}

	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to check for default branch existence, got unexpected response status code %d", resp.StatusCode)
	}

	if dryrun {
		m.logger.Infof("DRYRUN: Skipped executing API call [CreateBranch]")
	} else {
		if _, _, err := m.branchesClient.CreateBranch(project.ID, opt); err != nil {
			return fmt.Errorf("failed to create default branch %s: %v", *opt.Branch, err)
		}
	}

	return nil
}

// willChangeApprovalSettings takes two ProjectSettings, and confirms if the 2nd one changes the 1st
func (m *ProjectManager) willChangeApprovalSettings(current *gitlab.ProjectApprovals, changes *gitlab.ProjectApprovals) bool {
	changelog, _ := diff.Diff(current, changes)

	changeExpected := false
	m.logger.Debugf("%v", changelog)

	for _, change := range changelog {
		if change.Type == "update" {
			changeExpected = true
		}
	}

	return changeExpected
}

// willChangeProjectSettings takes two ProjectSettings, and confirms if the 2nd one changes the 1st
func (m *ProjectManager) willChangeProjectSettings(current *gitlab.Project, changes *gitlab.Project) bool {
	changelog, _ := diff.Diff(current, changes)

	changeExpected := false
	m.logger.Debugf("%v", changelog)

	for _, change := range changelog {
		if change.Type == "update" {
			changeExpected = true
		}
	}

	return changeExpected
}
