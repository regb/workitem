package web

import "testing"

func TestRemotesLinkKnownHostsWithoutCredentials(t *testing.T) {
	for _, test := range []struct{ raw, branch, url, web, host string }{
		{"git@github.com:regb/workitem.git", "main", "github.com:regb/workitem.git", "https://github.com/regb/workitem/tree/main", "github"},
		{"https://user:token@github.com/regb/workitem.git", "", "https://github.com/regb/workitem.git", "https://github.com/regb/workitem", "github"},
		{"ssh://git@gitlab.com/group/sub/app.git", "dev", "ssh://gitlab.com/group/sub/app.git", "https://gitlab.com/group/sub/app/-/tree/dev", "gitlab"},
		{"https://git.example.com/team/app.git", "main", "https://git.example.com/team/app.git", "", ""},
		{"/srv/git/app.git", "main", "/srv/git/app.git", "", ""},
	} {
		remote := NewRemote(test.raw, test.branch)
		if remote.URL != test.url || remote.WebURL != test.web || remote.Host != test.host {
			t.Errorf("%s: %+v", test.raw, remote)
		}
	}
	if NewRemote("", "main") != nil {
		t.Error("no URL is no remote")
	}
}
