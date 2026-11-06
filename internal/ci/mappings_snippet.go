package ci

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
)

// The CI snippet (R-API-035, R-UI-035; R-CI-027 is the broader P1 requirement): a `.gitlab-ci.yml` fragment
// for one mapping, built from the reference pipeline of R-CI-028 and the examples of the specification: the
// `id_tokens:` block with the issuer's first allowed audience, the exchange, the client configuration for
// npm or NuGet for the repositories the mapping's grants name, and the revocation as the last step. It holds
// no secret: a token never appears in it, only the variables of the job.

// Snippet kinds and formats.
const (
	SnippetGitLabCI = "gitlab-ci"
	FormatNPM       = "npm"
	FormatNuGet     = "nuget"
)

// The protocol addresses of a repository carry its project (R-ARCH-005): /api/<format>/<project>/<key>/.
const (
	npmBaseTemplate   = "/api/npm/{project}/{key}/"
	nugetBaseTemplate = "/api/nuget/{project}/{key}/"
)

// placeholderRepo stands for the repository the administrator names when the mapping's grants name none.
var placeholderRepo = qname.Repo{Project: "<project>", Key: "<repository>"}

// protocolBase is the base path of the repository in the template; the placeholder is written as it is.
func protocolBase(tmpl string, r qname.Repo) string {
	if r == placeholderRepo {
		return strings.NewReplacer("{project}", r.Project, "{key}", r.Key).Replace(tmpl)
	}
	return qname.ProtocolBase(tmpl, r)
}

// Snippet renders the CI snippet of a mapping as text/plain. The mapping must exist on the issuer, in the
// tier of req.Project (the system tier when it is empty); a project's form is the handler's call of GetIn first.
func (s *MappingService) Snippet(ctx context.Context, issuer string, req SnippetRequest) (string, error) {
	format := req.Format
	if format == "" {
		format = FormatNPM
	}
	var bad []errs.FieldError
	if req.Kind != SnippetGitLabCI {
		bad = append(bad, field("/kind", "invalid_value", "The snippet kinds of this release are gitlab-ci."))
	}
	if format != FormatNPM && format != FormatNuGet {
		bad = append(bad, field("/format", "invalid_value", "The formats are npm and nuget."))
	}
	if req.Mapping == "" {
		bad = append(bad, field("/mapping", "required", "Name the mapping the snippet is for."))
	}
	if err := mappingViolation(bad); err != nil {
		return "", err
	}
	tier := TierSystem
	if req.Project != "" {
		tier = ProjectTier(req.Project)
	}
	m, err := s.get(ctx, issuer, tier, req.Mapping)
	if err != nil {
		return "", err
	}
	var audience string
	var formats map[qname.Repo]string
	err = s.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.CiIssuerAudiences(ctx, issuer)
		if err != nil {
			return err
		}
		keys, _ := literalRepositories(m)
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		frows, err := qs.CiRepositoryFormats(ctx, names)
		if err != nil {
			return err
		}
		formats = map[qname.Repo]string{}
		for _, r := range frows {
			repo, err := qname.ParseRepo(r.Name)
			if err != nil {
				return err
			}
			formats[repo] = r.Format
		}
		if len(rows) == 0 {
			return notFound(issuer)
		}
		if rows[0].Kind != KindGitLab {
			return mappingViolation([]errs.FieldError{field("/kind", "kind_not_available", "The snippet of this release is for GitLab issuers.")})
		}
		if len(rows[0].AllowedAudiences) > 0 {
			audience = rows[0].AllowedAudiences[0]
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return renderSnippet(snippetInput{
		BaseURL: strings.TrimRight(s.o.BaseURL, "/"), Audience: audience, Issuer: issuer, Mapping: m, Format: format, Formats: formats,
	}), nil
}

type snippetInput struct {
	BaseURL  string
	Audience string
	Issuer   string
	Mapping  Mapping
	Format   string
	// Formats is the format of each literal repository of the mapping that exists; the snippet configures the
	// repositories of the client's format only.
	Formats map[qname.Repo]string
}

// literalRepositories are the repositories the mapping's inline grants name literally, in the order of the
// grants, each once, with whether any grant of that repository writes. A grant of a system mapping names
// `<project>/<key>`, one of a project's mapping the bare key of its project. A pattern, a class or a template
// names no repository the snippet could configure.
func literalRepositories(m Mapping) (keys []qname.Repo, writes map[qname.Repo]bool) {
	writes = map[qname.Repo]bool{}
	in := qname.Context{Project: m.Project}
	for _, ig := range m.Grants.Inline {
		for _, r := range ig.Repositories {
			if strings.Contains(r, "{{") {
				continue
			}
			sel, err := qname.ParseSelector(r, in)
			if err != nil || sel.Kind != qname.SelectorKey {
				continue
			}
			repo := qname.Repo{Project: sel.Project, Key: sel.Key}
			if _, seen := writes[repo]; !seen {
				keys = append(keys, repo)
				writes[repo] = false
			}
			writes[repo] = writes[repo] || ig.Writes()
		}
	}
	return keys, writes
}

func renderSnippet(in snippetInput) string {
	host := in.BaseURL
	if u, err := url.Parse(in.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	all, writes := literalRepositories(in.Mapping)
	var keys []qname.Repo
	for _, k := range all {
		if in.Formats[k] == in.Format {
			keys = append(keys, k)
		}
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("# trove: CI snippet for the mapping %q of the CI issuer %q (%s).", in.Mapping.Name, in.Issuer, in.Format)
	w("# It holds no secret. The ID token and the CI token exist only inside the job; the CI token is revoked as its last step.")
	w("stages: [publish]")
	w("")
	w("variables:")
	w("  TROVE_URL: %q", in.BaseURL)
	w("")
	w("# Hidden job: requests the ID token and exchanges it in before_script.")
	w(".trove:")
	w("  id_tokens:")
	w("    TROVE_ID_TOKEN:")
	w("      aud: %s", in.Audience)
	w("  before_script:")
	w("    - |")
	w("      if ! command -v jq >/dev/null || ! command -v curl >/dev/null; then")
	w("        apt-get update -qq && apt-get install -y -qq --no-install-recommends curl jq ca-certificates >/dev/null")
	w("      fi")
	w("    - |")
	w(`      response="$(printf '%%s' "$TROVE_ID_TOKEN" | curl -sS --retry 3 \`)
	w(`        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \`)
	w(`        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:id_token' \`)
	w(`        --data-urlencode 'subject_token@-' \`)
	w(`        "$TROVE_URL/api/v1/auth/oidc/token")"`)
	w(`      TROVE_TOKEN="$(printf '%%s' "$response" | jq -er '.access_token // empty')" \`)
	w(`        || { printf '%%s\n' "$response" | jq '{error, error_description, reason, requestId}' >&2; exit 1; }`)
	w("      export TROVE_TOKEN")
	w("      unset response TROVE_ID_TOKEN")
	w("")
	if in.Format == FormatNuGet {
		writeNuGetJob(w, in, host, keys, writes)
	} else {
		writeNPMJob(w, in, host, keys, writes)
	}
	return b.String()
}

// publishTarget is the repository the job publishes to: the first literal repository a grant writes to,
// else the first literal repository, else a placeholder the administrator names.
func publishTarget(keys []qname.Repo, writes map[qname.Repo]bool) (qname.Repo, bool) {
	for _, k := range keys {
		if writes[k] {
			return k, true
		}
	}
	if len(keys) > 0 {
		return keys[0], false
	}
	return placeholderRepo, false
}

func jobRules(m Mapping, w func(string, ...any)) {
	if m.Match.RequiresProtectedRef() {
		w("  rules:")
		w("    - if: $CI_COMMIT_REF_PROTECTED == \"true\"   # the mapping requires a protected ref")
	}
}

func writeNPMJob(w func(string, ...any), in snippetInput, host string, keys []qname.Repo, writes map[qname.Repo]bool) {
	target, canWrite := publishTarget(keys, writes)
	w("publish-npm:")
	w("  stage: publish")
	w("  image: node:24-bookworm   # example")
	w("  extends: .trove")
	jobRules(in.Mapping, w)
	w("  script:")
	w("    # npm expands ${TROVE_TOKEN} at run time; the token is never written to disk.")
	w("    - |")
	w("      cat > .npmrc <<'EOF'")
	w("      registry=%s%s", in.BaseURL, protocolBase(npmBaseTemplate, target))
	for _, k := range keys {
		w("      //%s%s:_authToken=${TROVE_TOKEN}", host, protocolBase(npmBaseTemplate, k))
	}
	if len(keys) == 0 {
		w("      //%s%s:_authToken=${TROVE_TOKEN}", host, protocolBase(npmBaseTemplate, target))
	}
	w("      EOF")
	w("    - npm ci")
	if canWrite {
		w("    - npm publish --registry %s%s", in.BaseURL, protocolBase(npmBaseTemplate, target))
	} else {
		w("    # The mapping's grants name no repository this job can publish to; add your npm commands here.")
	}
	revocation(w)
}

func writeNuGetJob(w func(string, ...any), in snippetInput, host string, keys []qname.Repo, writes map[qname.Repo]bool) {
	_ = host
	target, canWrite := publishTarget(keys, writes)
	w("push-nuget:")
	w("  stage: publish")
	w("  image: mcr.microsoft.com/dotnet/sdk:10.0   # example")
	w("  extends: .trove")
	jobRules(in.Mapping, w)
	w("  script:")
	w("    # nuget.config reads %%TROVE_TOKEN%% for restores; put this file in the repository root (developers use their personal access token in TROVE_TOKEN):")
	w("    #")
	w(`    #   <?xml version="1.0" encoding="utf-8"?>`)
	w("    #   <configuration>")
	w("    #     <packageSources>")
	w("    #       <clear />")
	w(`    #       <add key="trove" value="%s%sv3/index.json" />`, in.BaseURL, protocolBase(nugetBaseTemplate, target))
	w("    #     </packageSources>")
	w("    #     <packageSourceCredentials>")
	w("    #       <trove>")
	w(`    #         <add key="Username" value="ci" />`)
	w(`    #         <add key="ClearTextPassword" value="%%TROVE_TOKEN%%" />`)
	w("    #       </trove>")
	w("    #     </packageSourceCredentials>")
	w("    #   </configuration>")
	w("    - dotnet restore")
	w("    - dotnet pack -c Release -o out --no-restore -p:PackageVersion=\"${CI_COMMIT_TAG#v}\"")
	if canWrite {
		w("    - >-")
		w("      dotnet nuget push \"out/*.nupkg\"")
		w("      --source \"$TROVE_URL%sv3/index.json\"", protocolBase(nugetBaseTemplate, target))
		w("      --api-key \"$TROVE_TOKEN\"")
	} else {
		w("    # The mapping's grants name no repository this job can push to; add your dotnet commands here.")
	}
	revocation(w)
}

// revocation is the last line of `script`, not an after_script: after_script runs in a separate shell
// that does not see the variables exported in before_script (R-CI-028).
func revocation(w func(string, ...any)) {
	w("    # Last step: revoke the CI token, so it does not outlive the job (R-CI-017).")
	// A block scalar: a plain scalar with a colon and a space in it ("Authorization: Bearer") is a YAML mapping.
	w("    - |")
	w(`      printf 'Authorization: Bearer %%s\n' "$TROVE_TOKEN" | curl -sS -X DELETE -H @- "$TROVE_URL/api/v1/tokens/current"`)
}
