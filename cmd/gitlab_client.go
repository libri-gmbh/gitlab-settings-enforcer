package cmd

import "github.com/xanzy/go-gitlab"

func gitlabClient() (*gitlab.Client, error) {
	baseURL := "https://gitlab.com/"
	if env.GitlabEndpoint != "" {
		baseURL = env.GitlabEndpoint
	}
	client, err := gitlab.NewClient(env.GitlabToken, gitlab.WithBaseURL(baseURL))
	if err != nil {
		return nil, err
	}
	return client, nil
}
