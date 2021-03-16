// Package github provides a GitHub release store.
package github

import (
	"context"
	"github.com/BGrewell/go-update"
	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
	"net/http"
	"time"
)

// Store is the store implementation.
type Store struct {
	Owner   string
	Repo    string
	Version string
	Token   *string
}

// GetRelease returns the specified release or ErrNotFound.
func (s *Store) GetRelease(version string) (*update.Release, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var tc *http.Client
	if s.Token != nil {
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{
				AccessToken: *s.Token,
			},
		)
		tc = oauth2.NewClient(ctx, ts)
	}

	gh := github.NewClient(tc)

	r, res, err := gh.Repositories.GetReleaseByTag(ctx, s.Owner, s.Repo, "v"+version)

	if res.StatusCode == 404 {
		return nil, update.ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	return githubRelease(r), nil
}

// LatestReleases returns releases newer than Version, or nil.
func (s *Store) LatestReleases() (latest []*update.Release, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var tc *http.Client
	if s.Token != nil {
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{
				AccessToken: *s.Token,
			},
		)
		tc = oauth2.NewClient(ctx, ts)
	}

	gh := github.NewClient(tc)

	releases, _, err := gh.Repositories.ListReleases(ctx, s.Owner, s.Repo, nil)
	if err != nil {
		return nil, err
	}

	for _, r := range releases {
		tag := r.GetTagName()

		if tag == s.Version || "v"+s.Version == tag {
			break
		}

		latest = append(latest, githubRelease(r))
	}

	return
}

// githubRelease returns a Release.
func githubRelease(r *github.RepositoryRelease) *update.Release {
	out := &update.Release{
		Version:     r.GetTagName(),
		Notes:       r.GetBody(),
		PublishedAt: r.GetPublishedAt().Time,
		URL:         r.GetURL(),
	}

	for _, a := range r.Assets {
		out.Assets = append(out.Assets, &update.Asset{
			Name:      a.GetName(),
			Size:      a.GetSize(),
			URL:       a.GetBrowserDownloadURL(),
			Downloads: a.GetDownloadCount(),
		})
	}

	return out
}
