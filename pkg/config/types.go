package config

import (
	"errors"

	"github.com/xanzy/go-gitlab"
)

// GitLab AccessLevel string aliases used in the config
const (
	AccessLevelDeveloper  = "developer"
	AccessLevelMaintainer = "maintainer"
)

var (
	errFileDoesNotExist                      = errors.New("given config file does not exist")
	errOnlyOneOfBlacklistAndWhitelistAllowed = errors.New("only one is allowed: project_blacklist / project_whitelist")
	errProjectSettingsNameMustBeEmpty        = errors.New("project_settings.name must be empty")
)

// Config stores the root group name and some additional configuration values
// settings documented at https://godoc.org/github.com/xanzy/go-gitlab#CreateProjectOptions
type Config struct {
	GroupName           string `json:"group_name"`
	IncludeSubgroups    bool   `json:"include_subgroups"`
	CreateDefaultBranch bool   `json:"create_default_branch"`
	Error               bool
	ProjectBlacklist    []string          `json:"project_blacklist"`
	ProjectWhitelist    []string          `json:"project_whitelist"`
	ProtectedBranches   []ProtectedBranch `json:"protected_branches"`
	ProtectedTags       []ProtectedTag    `json:"protected_tags"`

	ApprovalSettings *gitlab.ChangeApprovalConfigurationOptions `json:"approval_settings"`
	ProjectSettings  *gitlab.EditProjectOptions                 `json:"project_settings"`
	Compliance       *ComplianceSettings                        `json:"compliance"`
}

// ComplianceSettings defines what is displayed and mandatory settings.
type ComplianceSettings struct {
	Email     EmailConfig                       `json:"email"`
	Mandatory map[string]map[string]interface{} `json:"mandatory"`
}

// EmailConfig
type EmailConfig struct {
	From   string
	Port   int
	Server string
	To     []string
}

// ProtectedBranch defines who can act on a protected branch
type ProtectedBranch struct {
	Name             string      `json:"name"`
	PushAccessLevel  AccessLevel `json:"push_access_level"`
	MergeAccessLevel AccessLevel `json:"merge_access_level"`
}

// ProtectedTag defines who can create a protected tag
type ProtectedTag struct {
	Name              string      `json:"name"`
	CreateAccessLevel AccessLevel `json:"create_access_level"`
}

// AccessLevel wraps the numeric gitlab access level into a readable string
type AccessLevel string

// Value returns the gitlab numeric value of the access level
func (a AccessLevel) Value() *gitlab.AccessLevelValue {
	switch a {
	case AccessLevelDeveloper:
		return gitlab.AccessLevel(gitlab.DeveloperPermissions)
	case AccessLevelMaintainer:
		return gitlab.AccessLevel(gitlab.MaintainerPermissions)
	default:
		return gitlab.AccessLevel(gitlab.NoPermissions)
	}
}
