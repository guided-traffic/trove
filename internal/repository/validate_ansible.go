package repository

import (
	"encoding/json"
	"net"
	"net/url"
	"path"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// The rules of the Ansible Galaxy format's member of a document that the document alone decides (R-ANS-019, Q-ANS-51): `ansible.authUrl`
// names the token endpoint of the realm where the repository's credential, a refresh token, is exchanged for the access token the hub
// is called with. The refresh token goes into the body of the request to that endpoint, where the hop policy cannot withhold it, so the
// document is held to what the engine enforces again at every exchange: the endpoint is an https URL (http only for a loopback address,
// the test stub's), the repository's authMode is bearer (the stored secret is the refresh token), and the endpoint's host is the
// upstream's or an extraHosts entry with sendCredentials. The policies `ignore` and `pass-through` are refused by the format's fact
// ChecksumAlwaysVerified, as for every format that announces digests.

const (
	ansibleFormat      = "ansible"
	ansibleAuthPointer = "/ansible/authUrl"
)

// validateAnsible checks ansible.authUrl of a remote repository.
func validateAnsible(v *validation, info catalog.Entry, d *Document) {
	if string(info.ID) != ansibleFormat || d.Class != Remote || len(d.Ansible) == 0 {
		return
	}
	var member struct {
		AuthURL string `json:"authUrl"`
	}
	if json.Unmarshal(d.Ansible, &member) != nil || member.AuthURL == "" {
		return
	}
	u, err := url.Parse(member.AuthURL)
	switch {
	case err != nil || u.Host == "" || u.Hostname() == "":
		v.add(ansibleAuthPointer, codeInvalidValue, "must be an absolute URL with a host")
		return
	case u.User != nil || u.Fragment != "" || u.RawQuery != "":
		v.add(ansibleAuthPointer, codeInvalidValue, "must carry no user information, query or fragment: the credential belongs in the repository's secret")
		return
	case u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())):
		v.add(ansibleAuthPointer, codeInvalidValue, "must be an https URL (http only for a loopback address): the refresh token is sent to it")
		return
	}
	var remote struct {
		URL        string `json:"url"`
		AuthMode   string `json:"authMode"`
		ExtraHosts []struct {
			Pattern         string `json:"pattern"`
			SendCredentials bool   `json:"sendCredentials"`
		} `json:"extraHosts"`
	}
	_ = json.Unmarshal(d.Remote, &remote) //nolint:errcheck // the remote section was checked before; one that does not read has no authMode
	if remote.AuthMode != AuthBearer {
		v.add(ansibleAuthPointer, codeInvalidValue, "needs remote.authMode bearer: the stored secret is the refresh token that is exchanged at this endpoint")
		return
	}
	host := strings.ToLower(u.Hostname())
	if up, err := url.Parse(remote.URL); err == nil && strings.EqualFold(up.Hostname(), host) {
		return
	}
	for _, h := range remote.ExtraHosts {
		if ok, _ := path.Match(strings.ToLower(h.Pattern), host); ok && h.SendCredentials {
			return
		}
	}
	v.add(ansibleAuthPointer, codeInvalidValue, "its host must be the upstream's or an extraHosts entry with sendCredentials: the refresh token goes there")
}

// loopbackHost reports whether host is localhost or a loopback address.
func loopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
