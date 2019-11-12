package gitlab

import (
  "fmt"
  "net/http"
  "net/url"
  "regexp"
  "strconv"
  "strings"

  "github.com/xanzy/go-gitlab"

  "github.com/erinkerNCS/gitlab-settings-enforcer/pkg/config"
  "github.com/erinkerNCS/gitlab-settings-enforcer/pkg/internal/stringslice"

  "github.com/sirupsen/logrus"
)

// ProjectManager fetches a list of repositories from GitLab
type ProjectManager struct {
  logger                  *logrus.Entry
  groupsClient            groupsClient
  projectsClient          projectsClient
  protectedBranchesClient protectedBranchesClient
  branchesClient          branchesClient
  config                  *config.Config
}

// NewProjectManager returns a new ProjectManager instance
func NewProjectManager(
  logger *logrus.Entry,
  groupsClient groupsClient,
  projectsClient projectsClient,
  protectedBranchesClient protectedBranchesClient,
  branchesClient branchesClient,
  config *config.Config,
) *ProjectManager {
  return &ProjectManager{
    logger:                  logger,
    groupsClient:            groupsClient,
    projectsClient:          projectsClient,
    protectedBranchesClient: protectedBranchesClient,
    branchesClient:          branchesClient,
    config:                  config,
  }
}

// GetSubgroupID walks the provided path. returning the Group ID of the last desired subgroup.
func (m *ProjectManager) GetSubgroupID(path string, indent int, group_ID int) (int, error) {
  var subgroup_ID int

  subpath := strings.Split(path, "/")[indent]
  path_count := len(strings.Split(path, "/"))-1
  m.logger.Debugf("Walking %s, looking for %s[%d/%d].", path, subpath, indent, path_count)

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

  if indent != path_count {
    m.logger.Debugf("Found Group ID %d, going deeper.", subgroup_ID)
    subgroup_ID, _ = m.GetSubgroupID(path, indent+1, subgroup_ID)
  }

  m.logger.Debugf("Coming back up from %s.", subpath)
  return subgroup_ID, nil
}

// GetProjects fetches a list of accessible repos within the groups set in config file
func (m *ProjectManager) GetProjects() ([]Project, error) {
  var repos []Project

  m.logger.Debugf("Fetching projects under %s path ...", m.config.GroupName)

  // Identify Group/Subgroup's ID
  var groupID int

  m.logger.Debugf("Identifying %s's GroupID", m.config.GroupName)
  if strings.ContainsAny(m.config.GroupName, "/") {
    // Nested Path
    group_ID, err := m.GetSubgroupID(m.config.GroupName, 1, 0)
    if err != nil {
      return []Project{}, fmt.Errorf("failed to fetch GitLab group info for %q: %v", m.config.GroupName, err)
    }
    groupID = group_ID
  } else {
    // BugFix: Without this pre-processing, go-gitlab library stalls.
    var group_name string = strings.Replace(url.PathEscape(m.config.GroupName), ".", "%2E", -1)
    group, _, err := m.groupsClient.GetGroup(group_name)
    if err != nil {
      return []Project{}, fmt.Errorf("failed to fetch GitLab group info for %q: %v", group_name, err)
    }
    groupID = group.ID
  }

  m.logger.Debugf("GroupID is %d", groupID)

  // Get Project objects
  for {
    projects, resp, err := m.groupsClient.ListGroupProjects(groupID, listGroupProjectOps, addIncludeSubgroups)
    if err != nil {
      return []Project{}, fmt.Errorf("failed to fetch GitLab projects for %s [%d]: %v", m.config.GroupName, groupID, err)
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

      repos = append(repos, Project{
        ID:       p.ID,
        Name:     p.Name,
        FullPath: p.PathWithNamespace,
      })
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

// EnsureBranchesAndProtection ensures that 1) the default branch exists and 2) all of the protected branches are configured correctly
func (m *ProjectManager) EnsureBranchesAndProtection(project Project, dryrun bool) error {
  if err := m.ensureDefaultBranch(project, dryrun); err != nil {
    return err
  }

  for _, b := range m.config.ProtectedBranches {
    if dryrun {
      m.logger.Infof("DRYRUN: Skipped executing API call [UnprotectRepositoryBranches] on %v branch.", b.Name)
      m.logger.Infof("DRYRUN: Skipped executing API call [ProtectRepositoryBranches] on %v branch.", b.Name)
      continue
    }

    // Remove protections (if present)
    if resp, err := m.protectedBranchesClient.UnprotectRepositoryBranches(project.ID, b.Name); err != nil && resp.StatusCode != http.StatusNotFound {
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

func (m *ProjectManager) ensureDefaultBranch(project Project, dryrun bool) error {
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

// UpdateProjectSettings updates the project settings on gitlab
func (m *ProjectManager) UpdateProjectSettings(project Project, dryrun bool) error {
  m.logger.Debugf("Updating settings of project %s ...", project.FullPath)

  if m.config.ProjectSettings == nil {
    return fmt.Errorf("No project_settings section provided in config")
  }

  m.logger.Debugf("---[ HTTP Payload ]---\n")
  m.logger.Debugf("%+v\n", m.config.ProjectSettings)

  var returned_project *gitlab.Project
  var response *gitlab.Response
  var err error
  if dryrun {
    m.logger.Infof("DRYRUN: Skipped executing API call [EditProject]")
  } else {
    returned_project, response, err = m.projectsClient.EditProject(project.ID, m.config.ProjectSettings)
  }

  m.logger.Debugf("---[ HTTP Response ]---\n")
  m.logger.Debugf("%v\n", response)
  m.logger.Debugf("---[ Returned Project ]---\n")
  m.logger.Debugf("%v\n", returned_project)

  if err != nil {
    return fmt.Errorf("failed to update settings of project %s: %v", project.FullPath, err)
  }

  m.logger.Debugf("Updating settings of project %s done.", project.FullPath)

  return nil
}

// UpdateProjectMergeRequestSettings updates the project settings on gitlab
func (m *ProjectManager) UpdateProjectApprovalSettings(project Project, dryrun bool) error {
  m.logger.Debugf("Updating merge request approval settings of project %s [%d]...", project.FullPath, project.ID)

  if m.config.ApprovalSettings == nil {
    return fmt.Errorf("No approval_settings section provided in config")
  }

  m.logger.Debugf("---[ HTTP Payload ]---\n")
  m.logger.Debugf("%+v\n", m.config.ApprovalSettings)

  var returned_mr *gitlab.ProjectApprovals
  var response *gitlab.Response
  var err error
  if dryrun {
    m.logger.Infof("DRYRUN: Skipped executing API call [ChangeApprovalConfiguration]")
  } else {
    returned_mr, response, err = m.projectsClient.ChangeApprovalConfiguration(project.ID, m.config.ApprovalSettings)
  }

  m.logger.Debugf("---[ HTTP Response ]---\n")
  m.logger.Debugf("%v\n", response)
  m.logger.Debugf("---[ Returned MR ]---\n")
  m.logger.Debugf("%v\n", returned_mr)

  if err != nil {
    return fmt.Errorf("failed to update merge request approval settings or project %s: %v", project.FullPath, err)
  }

  m.logger.Debugf("Updating merge request approval settings of project %s done.", project.FullPath)

  return nil
}

// GetError returns the Error status
func (m *ProjectManager) GetError() (bool) {
  return m.config.Error
}

// SetError returns the Error status
func (m *ProjectManager) SetError(state bool) (bool) {
  m.config.Error = state
  return m.config.Error
}
