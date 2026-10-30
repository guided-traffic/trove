package manifest

import (
	"regexp"
	"strings"
)

// gitHost is one hosted-git provider npm recognizes in a dependency range:
// its shortcut ("github:user/repo"), its domain and the URL protocols it takes.
type gitHost struct {
	name      string
	domain    string
	protocols []string
}

// gitHostList returns the hosts; a function, because the state rule of R-ARCH-032
// allows no package-level slice.
func gitHostList() []gitHost {
	return []gitHost{
		{"github", "github.com", []string{"git:", "http:", "git+ssh:", "git+https:", "ssh:", "https:"}},
		{"bitbucket", "bitbucket.org", []string{"git+ssh:", "git+https:", "ssh:", "https:"}},
		{"gitlab", "gitlab.com", []string{"git+ssh:", "git+https:", "ssh:", "https:"}},
		{"sourcehut", "git.sr.ht", []string{"git+ssh:", "https:"}},
	}
}

func hostByShortcut(proto string) *gitHost {
	hosts := gitHostList()
	for i := range hosts {
		if hosts[i].name+":" == proto {
			return &hosts[i]
		}
	}
	return nil
}

func hostByDomain(domain string) *gitHost {
	domain = strings.TrimPrefix(strings.ToLower(domain), "www.")
	hosts := gitHostList()
	for i := range hosts {
		if hosts[i].domain == domain {
			return &hosts[i]
		}
	}
	return nil
}

func (h *gitHost) takes(proto string) bool {
	for _, p := range h.protocols {
		if p == proto {
			return true
		}
	}
	return false
}

// CanonicalGitSpec returns npm's canonical form of a hosted-git dependency
// range, the form hosted-git-info's toString() gives: "user/repo" becomes
// "github:user/repo", "https://github.com/user/repo" becomes
// "git+https://github.com/user/repo.git", and "git@github.com:user/repo.git"
// becomes "git+ssh://git@github.com/user/repo.git". Anything else (semver
// ranges, tags, file: and other URLs, hosts npm does not know) is returned
// unchanged, and so is a spec that would need percent-decoding.
func CanonicalGitSpec(spec string) string {
	if spec == "" || strings.ContainsAny(spec, "% \t\r\n\\") {
		return spec
	}
	s := spec
	if isGitHubShorthand(s) {
		s = "github:" + s
	}
	if out, ok := canonicalGitURL(s); ok {
		return out
	}
	return spec
}

// isGitHubShorthand is hosted-git-info's shorthand test for "user/repo".
func isGitHubShorthand(arg string) bool {
	firstHash := strings.IndexByte(arg, '#')
	firstSlash := strings.IndexByte(arg, '/')
	secondSlash := -1
	if firstSlash >= 0 {
		if i := strings.IndexByte(arg[firstSlash+1:], '/'); i >= 0 {
			secondSlash = firstSlash + 1 + i
		}
	}
	firstColon := strings.IndexByte(arg, ':')
	firstAt := strings.IndexByte(arg, '@')
	afterHash := func(i int) bool { return i == -1 || (firstHash > -1 && i > firstHash) }

	var doesNotEndWithSlash bool
	if firstHash > -1 {
		doesNotEndWithSlash = firstHash == 0 || arg[firstHash-1] != '/'
	} else {
		doesNotEndWithSlash = !strings.HasSuffix(arg, "/")
	}
	return firstSlash > 0 && doesNotEndWithSlash && !strings.HasPrefix(arg, ".") &&
		afterHash(firstAt) && afterHash(firstColon) && afterHash(secondSlash)
}

var digitsOnly = regexp.MustCompile(`^[0-9]*$`)

// canonicalGitURL parses the shortcut, URL and scp-style forms of the four
// hosts and renders the representation of the protocol it was given.
func canonicalGitURL(s string) (string, bool) {
	// Shortcut: "github:user/repo#committish".
	if i := strings.IndexByte(s, ':'); i > 0 {
		proto := strings.ToLower(s[:i+1])
		if h := hostByShortcut(proto); h != nil {
			rest := s[i+1:]
			if strings.HasPrefix(rest, "//") {
				return "", false
			}
			pathPart, hash, _ := strings.Cut(rest, "#")
			pathPart = strings.TrimPrefix(pathPart, "/")
			if at := strings.IndexByte(pathPart, '@'); at > -1 {
				pathPart = pathPart[at+1:]
			}
			slash := strings.LastIndexByte(pathPart, '/')
			if slash < 1 {
				return "", false
			}
			user, project := pathPart[:slash], strings.TrimSuffix(pathPart[slash+1:], ".git")
			if project == "" {
				return "", false
			}
			return h.name + ":" + user + "/" + project + joinHash(hash), true
		}
	}

	// URL or scp form, reduced to protocol, userinfo, host, path and hash.
	var proto, userinfo, host, path string
	body, hash, _ := strings.Cut(s, "#")
	if i := strings.Index(body, "://"); i > 0 {
		proto = strings.ToLower(body[:i+1])
		rest := body[i+3:]
		end := strings.IndexAny(rest, "/?")
		authority := rest
		path = ""
		if end >= 0 {
			authority, path = rest[:end], rest[end:]
		}
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			userinfo, authority = authority[:at], authority[at+1:]
		}
		host = authority
		if c := strings.LastIndexByte(authority, ':'); c >= 0 {
			if digitsOnly.MatchString(authority[c+1:]) {
				host = authority[:c]
			} else {
				// "git+ssh://git@github.com:user/repo.git": the last colon starts the path.
				host, path = authority[:c], "/"+authority[c+1:]+path
			}
		}
	} else if at := strings.IndexByte(body, '@'); at > 0 && strings.IndexByte(body, ':') > at {
		// scp style: "git@github.com:user/repo.git".
		proto = "git+ssh:"
		c := strings.IndexByte(body, ':')
		userinfo, host, path = body[:at], body[at+1:c], "/"+body[c+1:]
	} else {
		return "", false
	}
	if strings.ContainsAny(path, "?") {
		return "", false
	}
	h := hostByDomain(host)
	if h == nil || !h.takes(proto) {
		return "", false
	}
	user, project, committish, ok := extractGit(h, path, hash)
	if !ok || strings.Contains(user+project+committish, "%") {
		return "", false
	}

	auth := ""
	switch proto {
	case "git+https:", "https:", "git:", "http:":
		auth = userinfo
	}
	switch {
	case proto == "git+ssh:" || proto == "ssh:" || proto == "http:":
		return "git+ssh://git@" + h.domain + "/" + user + "/" + project + ".git" + joinHash(committish), true
	case proto == "git:":
		return "git://" + joinAuth(auth) + h.domain + "/" + user + "/" + project + ".git" + joinHash(committish), true
	case h.name == "sourcehut":
		return "https://" + h.domain + "/" + user + "/" + project + joinHash(committish), true
	default:
		return "git+https://" + joinAuth(auth) + h.domain + "/" + user + "/" + project + ".git" + joinHash(committish), true
	}
}

func joinHash(c string) string {
	if c == "" {
		return ""
	}
	return "#" + c
}

func joinAuth(a string) string {
	if a == "" {
		return ""
	}
	return a + "@"
}

// extractGit is the per-host extraction of user, project and committish from a
// URL path ("/user/project/..."), as hosted-git-info's hosts do.
func extractGit(h *gitHost, path, hash string) (user, project, committish string, ok bool) {
	split := func(n int) []string {
		parts := strings.SplitN(path, "/", n)
		// JS split(sep, n) truncates, SplitN keeps the rest in the last element.
		if len(parts) == n {
			parts[n-1], _, _ = strings.Cut(parts[n-1], "/")
		}
		for len(parts) < n {
			parts = append(parts, "")
		}
		return parts
	}
	switch h.name {
	case "github":
		p := split(5)
		user, project, typ := p[1], p[2], p[3]
		committish = p[4]
		if typ != "" && typ != "tree" {
			return "", "", "", false
		}
		if typ == "" {
			committish = hash
		}
		project = strings.TrimSuffix(project, ".git")
		if user == "" || project == "" {
			return "", "", "", false
		}
		return user, project, committish, true
	case "bitbucket", "sourcehut":
		p := split(4)
		user, project = p[1], strings.TrimSuffix(p[2], ".git")
		aux := p[3]
		if (h.name == "bitbucket" && aux == "get") || (h.name == "sourcehut" && aux == "archive") {
			return "", "", "", false
		}
		if user == "" || project == "" {
			return "", "", "", false
		}
		return user, project, hash, true
	default: // gitlab
		pth := strings.TrimPrefix(path, "/")
		if strings.Contains(pth, "/-/") || strings.Contains(pth, "/archive.tar.gz") {
			return "", "", "", false
		}
		seg := strings.Split(pth, "/")
		project = strings.TrimSuffix(seg[len(seg)-1], ".git")
		user = strings.Join(seg[:len(seg)-1], "/")
		if user == "" || project == "" {
			return "", "", "", false
		}
		return user, project, hash, true
	}
}
