package repo_downloader

import (
	"context"

	"github.com/go-git/go-git/v5/config"

	git "github.com/go-git/go-git/v5"
	memory "github.com/go-git/go-git/v5/storage/memory"
	gitutil "github.com/ninja-cloudbuild/cloudbuild/server/util/git"
)

type gitRepoDownloader struct{}

func NewRepoDownloader() *gitRepoDownloader {
	return &gitRepoDownloader{}
}

func (d *gitRepoDownloader) TestRepoAccess(ctx context.Context, repoURL, username, accessToken string) error {
	authURL, err := gitutil.AuthRepoURL(repoURL, username, accessToken)
	if err != nil {
		return err
	}

	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: repoURL,
		URLs: []string{authURL},
	})
	_, err = remote.List(&git.ListOptions{})
	return err
}
