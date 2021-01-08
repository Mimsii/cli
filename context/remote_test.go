package context

import (
	"errors"
	"net/url"
	"testing"

	"github.com/cli/cli/git"
	"github.com/cli/cli/internal/ghrepo"
	"github.com/cli/cli/test"
)

func Test_Remotes_FindByName(t *testing.T) {
	list := Remotes{
		&Remote{Remote: &git.Remote{Name: "mona"}, Repo: ghrepo.New("monalisa", "myfork")},
		&Remote{Remote: &git.Remote{Name: "origin"}, Repo: ghrepo.New("monalisa", "octo-cat")},
		&Remote{Remote: &git.Remote{Name: "upstream"}, Repo: ghrepo.New("hubot", "tools")},
	}

	r, err := list.FindByName("upstream", "origin")
	test.Eq(t, err, nil)
	test.Eq(t, r.Name, "upstream")

	r, err = list.FindByName("nonexistent", "*")
	test.Eq(t, err, nil)
	test.Eq(t, r.Name, "mona")

	_, err = list.FindByName("nonexistent")
	test.Eq(t, err, errors.New(`no GitHub remotes found`))
}

func Test_translateRemotes(t *testing.T) {
	publicURL, _ := url.Parse("https://github.com/monalisa/hello")
	originURL, _ := url.Parse("http://example.com/repo")

	gitRemotes := git.RemoteSet{
		&git.Remote{
			Name:     "origin",
			FetchURL: originURL,
		},
		&git.Remote{
			Name:     "public",
			FetchURL: publicURL,
		},
	}

	identityURL := func(u *url.URL) *url.URL {
		return u
	}
	result := TranslateRemotes(gitRemotes, identityURL)

	if len(result) != 1 {
		t.Errorf("got %d results", len(result))
	}
	if result[0].Name != "public" {
		t.Errorf("got %q", result[0].Name)
	}
	if result[0].RepoName() != "hello" {
		t.Errorf("got %q", result[0].RepoName())
	}
}
