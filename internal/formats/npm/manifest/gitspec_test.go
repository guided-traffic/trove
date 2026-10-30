package manifest

import "testing"

// The expectations were produced by hosted-git-info's fromUrl(spec).toString()
// of the version that ships with npm 11.17.0 (9.0.3); a spec it does not
// recognize stays as it is.
func TestCanonicalGitSpec(t *testing.T) {
	cases := []struct{ in, want string }{
		{"user/repo", "github:user/repo"},
		{"github:user/repo", "github:user/repo"},
		{"github:user/repo#v1", "github:user/repo#v1"},
		{"github:user/repo#semver:^1.0", "github:user/repo#semver:^1.0"},
		{"github:user/repo.git", "github:user/repo"},
		{"github:user/repo#", "github:user/repo"},
		{"gitlab:grp/sub/repo", "gitlab:grp/sub/repo"},
		{"gitlab:u/r", "gitlab:u/r"},
		{"bitbucket:u/r", "bitbucket:u/r"},
		{"sourcehut:~u/r", "sourcehut:~u/r"},
		{"sourcehut:~u/r#v2", "sourcehut:~u/r#v2"},
		{"git+ssh://git@github.com/u/r.git", "git+ssh://git@github.com/u/r.git"},
		{"git+ssh://git@github.com/u/r", "git+ssh://git@github.com/u/r.git"},
		{"git+ssh://git@github.com:u/r.git", "git+ssh://git@github.com/u/r.git"},
		{"git+ssh://github.com/u/r.git", "git+ssh://git@github.com/u/r.git"},
		{"ssh://git@github.com/u/r.git", "git+ssh://git@github.com/u/r.git"},
		{"ssh://git@github.com:22/u/r.git", "git+ssh://git@github.com/u/r.git"},
		{"git+https://github.com/u/r.git#abc", "git+https://github.com/u/r.git#abc"},
		{"git+https://github.com/u/r", "git+https://github.com/u/r.git"},
		{"git+https://user:pw@github.com/u/r.git", "git+https://user:pw@github.com/u/r.git"},
		{"https://github.com/u/r", "git+https://github.com/u/r.git"},
		{"https://github.com/u/r.git", "git+https://github.com/u/r.git"},
		{"https://github.com/u/r.git/", "git+https://github.com/u/r.git"},
		{"https://github.com/u/r#frag", "git+https://github.com/u/r.git#frag"},
		{"https://github.com/u/r/tree/main", "git+https://github.com/u/r.git#main"},
		{"https://github.com/u/r/blob/main/x", "https://github.com/u/r/blob/main/x"},
		{"https://www.github.com/u/r", "git+https://github.com/u/r.git"},
		{"https://GitHub.com/u/r", "git+https://github.com/u/r.git"},
		{"http://github.com/u/r", "git+ssh://git@github.com/u/r.git"},
		{"git://github.com/u/r.git", "git://github.com/u/r.git"},
		{"git://github.com/u/r", "git://github.com/u/r.git"},
		{"git://user@github.com/u/r.git#c", "git://user@github.com/u/r.git#c"},
		{"git@github.com:u/r.git", "git+ssh://git@github.com/u/r.git"},
		{"git@github.com:u/r.git#v1", "git+ssh://git@github.com/u/r.git#v1"},
		{"git@github.com:u/r", "git+ssh://git@github.com/u/r.git"},
		{"git@gitlab.com:g/s/r.git", "git+ssh://git@gitlab.com/g/s/r.git"},
		{"git@bitbucket.org:u/r.git", "git+ssh://git@bitbucket.org/u/r.git"},
		{"git@git.sr.ht:~u/r", "git+ssh://git@git.sr.ht/~u/r.git"},
		{"https://gitlab.com/g/s/r", "git+https://gitlab.com/g/s/r.git"},
		{"https://gitlab.com/g/s/r.git#c", "git+https://gitlab.com/g/s/r.git#c"},
		{"https://gitlab.com/g/r/-/archive/x", "https://gitlab.com/g/r/-/archive/x"},
		{"ssh://git@gitlab.com/g/s/r.git", "git+ssh://git@gitlab.com/g/s/r.git"},
		{"git+https://gitlab.com/g/s/r.git#c", "git+https://gitlab.com/g/s/r.git#c"},
		{"git+ssh://git@gitlab.com/g/r.git", "git+ssh://git@gitlab.com/g/r.git"},
		{"https://bitbucket.org/u/r.git", "git+https://bitbucket.org/u/r.git"},
		{"https://bitbucket.org/u/r/get/x.tar.gz", "https://bitbucket.org/u/r/get/x.tar.gz"},
		{"git+ssh://git@bitbucket.org/u/r.git", "git+ssh://git@bitbucket.org/u/r.git"},
		{"https://git.sr.ht/~u/r", "https://git.sr.ht/~u/r"},
		{"https://git.sr.ht/~u/r.git#v1", "https://git.sr.ht/~u/r#v1"},
		{"git+ssh://git@git.sr.ht/~u/r", "git+ssh://git@git.sr.ht/~u/r.git"},
		{"git+https://git.sr.ht/~u/r", "git+https://git.sr.ht/~u/r"},
		{"ssh://git@git.sr.ht/~u/r", "ssh://git@git.sr.ht/~u/r"},
		{"git://gitlab.com/g/r.git", "git://gitlab.com/g/r.git"},
		{"http://gitlab.com/g/r", "http://gitlab.com/g/r"},
		{"User/Repo", "github:User/Repo"},
		{"a.b/c", "github:a.b/c"},
		{"-a/b", "github:-a/b"},
		{"./a/b", "./a/b"},
		{"a/b/c", "a/b/c"},
		{"@s/p", "@s/p"},
		{"1.0.0", "1.0.0"},
		{"^1.2.3", "^1.2.3"},
		{"latest", "latest"},
		{"npm:foo@1", "npm:foo@1"},
		{"file:../x", "file:../x"},
		{"https://example.com/u/r", "https://example.com/u/r"},
		{"git+https://example.com/u/r.git", "git+https://example.com/u/r.git"},
		{"workspace:*", "workspace:*"},
		{"link:../x", "link:../x"},
		{"", ""},
		{"*", "*"},
		{"1.x || 2.x", "1.x || 2.x"},
		{"git+file:///tmp/x", "git+file:///tmp/x"},
		{"https://github.com/u", "https://github.com/u"},
		{"https://github.com/", "https://github.com/"},
		{"github:u", "github:u"}, // npm writes "github:null/u"; trove leaves a range it cannot place alone
		{"github:/u/r", "github:u/r"},
		{"github:u/r/x", "github:u/r/x"},
		{"u/r#c", "github:u/r#c"},
		{"u/r#c/d", "github:u/r#c/d"},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 "+c.in, func(t *testing.T) {
			got := CanonicalGitSpec(c.in)
			if got != c.want {
				t.Fatalf("CanonicalGitSpec(%q) = %q, want %q", c.in, got, c.want)
			}
			if again := CanonicalGitSpec(got); again != got {
				t.Fatalf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}
