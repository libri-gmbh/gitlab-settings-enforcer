# GitLab Settings Enforcer

Enforces GitLab project settings by reading a config file and talking to
the GitLab API(s).

# Usage

This project get's built automatically and is usable as a docker image at hub.docker.com: [`libri/gitlab-settings-enforcer`](https://hub.docker.com/r/libri/gitlab-settings-enforcer)

Create a configuration file config.json and run with `project-settings-enforcer sync`

To support multiple configuration files you can use this script:

```shell script
#!/bin/bash

set -ex

for file in configs/*; do
    if [[ ! -f $file ]]; then
      # skip non-files
      continue
    fi

    CONFIG_FILE="${file}" ./project-settings-enforcer sync

done
```

# Configuration

Configuration of project interaction is currently possible via JSON files
providing a Config object. The config object has the following fields:


| Field                   | Type              | Required | Content                                                                                                          | Default |
|-------------------------|-------------------|----------|------------------------------------------------------------------------------------------------------------------|---------|
| `group_name`            | string            | yes      | The path of the root group<BR>(e.g. `example` or `some/nested/example`)                                          |         |
| `project_blacklist`     | []string          | no       | A list of projects to blacklist<BR>(cannot be set when project_whitelist is used)                                | []      |
| `project_whitelist`     | []string          | no       | A list of projects to whitelist<BR>(cannot be set when project_blacklist is used)                                | []      |
| `create_default_branch` | bool              | no       | Whether the default branch configured in `project_settings.default_branch` should be created if it doesn't exist |         |
| `protected_branches`    | []ProtectedBranch | no       | A list of branches to protect, together with the infos which roles are allowed to merge or push.                 |         |
| `approval_settings`     | Object            | no       | The gitlab project approval settings to change. [Possible keys](https://docs.gitlab.com/ee/api/merge_request_approvals.html#change-configuration) |         |
| `project_settings`      | Object            | no       | The gitlab project settings to change. [Possible keys](https://docs.gitlab.com/ce/api/projects.html#edit-project) |         |
| `compliance`            | Object            | no       | The compliance configuration.                                                                                    |         |

`ProtectedBranch` 

| Field                | Type   | Required | Content                                                                              |
|----------------------|--------|----------|--------------------------------------------------------------------------------------|
| `name`               | string | yes      | The name of the branch to protect                                                    |
| `push_access_level`  | string | yes      | Which role is allowed to push (possible values: `maintainer`, `developer`, `noone`)  |
| `merge_access_level` | string | yes      | Which role is allowed to merge (possible values: `maintainer`, `developer`, `noone`) |

`Compliance` (Config for the Complience Report, run with `project-settings-enforcer complience`)

| Field                | Type   | Required | Content                                                                              |
|----------------------|--------|----------|--------------------------------------------------------------------------------------|
| `mandatory`          | Object | yes      | Setting names, and their values following the sync naming schema                     |
| `email`              | Object | no       | Email setting to send the complience Report                                                  |

`Email`

| Field                | Type     | Required | Content                                                                              |
|----------------------|----------|----------|--------------------------------------------------------------------------------------|
| `From`               | string   | yes      | From address                                                                         |
| `Server`             | string   | yes      | Smtpserver hostname                                                                  |
| `Port`               | int      | yes      | Smtpserver port                                                                      |
| `To`                 | []string | yes      | Recepients                                                                           |


## Env vars

To control the GitLab API endpoint and the authentication as well as further
internal flags please use the following env vars:

| Name              | Required | Description                                                                       | Default      |
|-------------------|----------|-----------------------------------------------------------------------------------|--------------|
| `GITLAB_ENDPOINT` | no       | Only override when using GitLab on premise, set this to your GitLab Server Domain | (gitlab.com) |
| `GITLAB_TOKEN`    | yes      | The GitLab API token used for authentication                                      |              |
| `VERBOSE`         | no       | Enables debug logging when enabled                                                | `false`      |
| `DRYRUN`          | no       | Only output the changes without setting them on gitlab                            | `false`      |

## Config Example

An example SYNC config might look like the following:

```json
{
  "group_name": "example",
  "project_blacklist": [
    "example/path-to/ignored-project"
  ],
  "project_whitelist": [],
  "create_default_branch": true,
  "protected_branches": [
    { "name": "develop", "push_access_level": "maintainer", "merge_access_level": "developer"},
    { "name": "master", "push_access_level": "maintainer", "merge_access_level": "developer"}
  ],
  "approval_settings": {
    "disable_overriding_approvers_per_merge_request": false,
    "merge_requests_author_approval": false,
    "merge_requests_disable_committers_approval": true,
    "reset_approvals_on_push": true
  },
  "project_settings": {
    "default_branch": "develop",
    "issues_enabled": true,
    "merge_requests_enabled": true,
    "jobs_enabled": true,
    "wiki_enabled": false,
    "snippets_enabled": false,
    "resolve_outdated_diff_discussions": true,
    "container_registry_enabled": true,
    "shared_runners_enabled": false,
    "only_allow_merge_if_pipeline_succeeds": false,
    "only_allow_merge_if_all_discussions_are_resolved": true,
    "merge_method": "merge",
    "public_builds": false,
    "lfs_enabled": true,
    "request_access_enabled": false,
    "tag_list": [],
    "printing_merge_request_link_enabled": true,
    "ci_config_path": null,
    "approvals_before_merge": 1
  }
}
```

An example COMPLIANCE config might look like the following:

```json
{
  "compliance": {
    "mandatory": {
      "approval_settings": {
        "reset_approvals_on_push": false
      },
      "project_settings": {
        "resolve_outdated_diff_discussions": false
      }
    }
  }
}
```

# License

    MIT License
    
    Copyright (c) 2019 Scalify GmbH
    Copyright (c) 2019 Eric Rinker
    Copyright (c) 2020 Libri GmbH
