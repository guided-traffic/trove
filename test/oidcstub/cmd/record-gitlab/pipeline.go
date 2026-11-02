package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The audiences the jobs ask for. The signature check's audience names no service, so the one raw
// token that leaves a job (as an artifact, deleted after the check) is addressed to nobody.
const (
	audience          = "https://packages.example.com"
	secondAudience    = "https://vault.example.com"
	defaultAudience   = "https://default.example.com"
	signatureAudience = "https://signature-check.invalid"
	signatureArtifact = "signature-check.jwt"
)

// ciConfig is the .gitlab-ci.yml of the recording. The jobs print, for each ID token variable,
// the number of dots and the base64url-decoded header and payload (the first two parts), and for
// CI_JOB_TOKEN only its length, the prefix up to the first "-" or "." and the number of dots.
// Nothing prints a raw token or a signature. GIT_STRATEGY none: no job needs the repository.
func ciConfig(runID, image string) string {
	return strings.NewReplacer("{{RUN}}", runID, "{{IMAGE}}", image, "{{AUD}}", audience, "{{AUD2}}", secondAudience,
		"{{DEFAULT_AUD}}", defaultAudience, "{{SIG_AUD}}", signatureAudience, "{{SIG_FILE}}", signatureArtifact).Replace(ciTemplate)
}

const ciTemplate = `# Written by trove's test/oidcstub/cmd/record-gitlab, run {{RUN}}. Every job prints the decoded
# header and payload of its ID tokens; no job prints a raw token or a signature.
workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_COMMIT_TAG
    - if: $CI_COMMIT_BRANCH

default:
  image: {{IMAGE}}
  id_tokens:
    DEFAULT_ID_TOKEN:
      aud: {{DEFAULT_AUD}}

variables:
  GIT_STRATEGY: none

.record: &record |
  b64d() { s=$(printf '%s' "$1" | tr '_-' '/+'); case $((${#s} % 4)) in 2) s="$s==";; 3) s="$s=";; esac; printf '%s' "$s" | base64 -d; }
  show() {
    eval "tok=\${$1:-}"
    if [ -z "$tok" ]; then printf 'OIDCV|%s|absent|-\n' "$1"; return 0; fi
    rest=${tok#*.}
    printf 'OIDCV|%s|dots|%s\n' "$1" "$(printf '%s' "$tok" | tr -cd . | wc -c | tr -d ' ')"
    printf 'OIDCV|%s|header|%s\n' "$1" "$(b64d "${tok%%.*}")"
    printf 'OIDCV|%s|payload|%s\n' "$1" "$(b64d "${rest%%.*}")"
  }
  jobtoken() {
    t=${CI_JOB_TOKEN:-}
    p=${t%%[-.]*}
    if [ "$p" = "$t" ]; then p=; fi
    printf 'OIDCV|CI_JOB_TOKEN|shape|%s %s %s\n' "${#t}" "${p:--}" "$(printf '%s' "$t" | tr -cd . | wc -c | tr -d ' ')"
  }
  printf 'OIDCV|job|started|%s\n' "$(date +%s)"
  printf 'OIDCV|job|timeout|%s\n' "${CI_JOB_TIMEOUT:--}"

.always:
  rules:
    - when: on_success

.main:
  rules:
    - if: $CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH

.tokens: &tokens
  TROVE_ID_TOKEN:
    aud: {{AUD}}

claims:
  extends: .always
  id_tokens:
    TROVE_ID_TOKEN:
      aud: {{AUD}}
    ARRAY_ID_TOKEN:
      aud: [{{AUD}}, {{AUD2}}]
  script:
    - *record
    - show TROVE_ID_TOKEN
    - show ARRAY_ID_TOKEN
    - show DEFAULT_ID_TOKEN
    - jobtoken

claims-default:
  extends: .main
  script:
    - *record
    - show DEFAULT_ID_TOKEN
    - show TROVE_ID_TOKEN

claims-timeout:
  extends: .main
  timeout: 15m
  id_tokens: *tokens
  script:
    - *record
    - show TROVE_ID_TOKEN

environment-start:
  rules:
    - if: $CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
    - if: $CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_TAG
  environment:
    name: production
    action: start
  id_tokens: *tokens
  script:
    - *record
    - show TROVE_ID_TOKEN

environment-prepare:
  extends: .main
  environment:
    name: production
    action: prepare
  id_tokens: *tokens
  script:
    - *record
    - show TROVE_ID_TOKEN

environment-unprotected:
  extends: .main
  environment:
    name: staging
    action: start
  id_tokens: *tokens
  script:
    - *record
    - show TROVE_ID_TOKEN

signature-check:
  extends: .main
  timeout: 10m
  id_tokens:
    SIGNATURE_ID_TOKEN:
      aud: {{SIG_AUD}}
  script:
    - printf '%s' "$SIGNATURE_ID_TOKEN" > {{SIG_FILE}}
  artifacts:
    paths: [{{SIG_FILE}}]
    expire_in: 1 hour
`

// pipe is one started pipeline.
type pipe struct {
	key       string
	projectID int64
	id        int64
	status    string
}

type pipelineInfo struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	SHA       string `json:"sha"`
	Ref       string `json:"ref"`
}

type commitAction struct {
	Action   string `json:"action"`
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// commit writes files on a branch (creating it from start, in startProject when not zero) and
// returns the commit's SHA.
func commit(ctx context.Context, c *client, projectID int64, branch, start string, startProject int64, msg string, actions []commitAction) (string, error) {
	in := map[string]any{"branch": branch, "commit_message": msg, "actions": actions}
	if start != "" {
		in["start_branch"] = start
	}
	if startProject != 0 {
		in["start_project"] = startProject
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.call(ctx, http.MethodPost, projectPath(projectID)+"/repository/commits", in, &out)
	return out.ID, err
}

// findPipeline waits until a pipeline of the query exists (pipelines of a push are created
// asynchronously) and returns the newest.
func findPipeline(ctx context.Context, c *client, projectID int64, query url.Values) (pipelineInfo, error) {
	query.Set("order_by", "id")
	query.Set("sort", "desc")
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var list []pipelineInfo
		if err := c.call(ctx, http.MethodGet, projectPath(projectID)+"/pipelines?"+query.Encode(), nil, &list); err != nil {
			return pipelineInfo{}, err
		}
		if len(list) > 0 {
			return list[0], nil
		}
		if time.Now().After(deadline) {
			return pipelineInfo{}, fmt.Errorf("no pipeline in project %d for %s after 3 minutes (are CI/CD and instance runners enabled?)", projectID, query.Encode())
		}
		if err := pause(ctx, 3*time.Second); err != nil {
			return pipelineInfo{}, err
		}
	}
}

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func latestPipelineID(ctx context.Context, c *client, projectID int64) (int64, error) {
	var list []pipelineInfo
	err := c.call(ctx, http.MethodGet, projectPath(projectID)+"/pipelines?per_page=1&order_by=id&sort=desc", nil, &list)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	return list[0].ID, nil
}

// startPipelines starts one pipeline per pipeline key of the cases.
func startPipelines(ctx context.Context, c *client, s setup, runID, config string) (map[string]*pipe, error) {
	pipes := map[string]*pipe{}
	add := func(key string, p pipelineInfo, projectID int64) {
		logf("pipeline %-15s project %d pipeline %d (%s, %s)", key, projectID, p.ID, p.Source, p.Ref)
		pipes[key] = &pipe{key: key, projectID: projectID, id: p.ID, status: p.Status}
	}
	pp := projectPath(s.parent.ID)
	marker := func(name string) []commitAction {
		return []commitAction{{Action: "create", FilePath: "rec/" + runID + "-" + name + ".txt", Content: runID + "\n"}}
	}

	// The configuration on main is a push pipeline on the protected default branch.
	action := "update"
	if err := c.call(ctx, http.MethodGet, pp+"/repository/files/"+url.PathEscape(".gitlab-ci.yml")+"?ref=main", nil, nil); isNotFound(err) {
		action = "create"
	} else if err != nil {
		return nil, err
	}
	sha, err := commit(ctx, c, s.parent.ID, "main", "", 0, "ci: record run "+runID,
		[]commitAction{{Action: action, FilePath: ".gitlab-ci.yml", Content: config}})
	if err != nil {
		return nil, err
	}
	p, err := findPipeline(ctx, c, s.parent.ID, q("sha", sha, "ref", "main", "source", "push"))
	if err != nil {
		return nil, err
	}
	add("main", p, s.parent.ID)

	branch := "rec/" + runID + "-unprotected"
	bsha, err := commit(ctx, c, s.parent.ID, branch, "main", 0, "rec: unprotected branch", marker("unprotected"))
	if err != nil {
		return nil, err
	}
	if p, err = findPipeline(ctx, c, s.parent.ID, q("sha", bsha, "ref", branch, "source", "push")); err != nil {
		return nil, err
	}
	add("branch", p, s.parent.ID)

	tag := "v1.4.0-rec." + runID
	if err := c.call(ctx, http.MethodPost, pp+"/repository/tags", map[string]any{"tag_name": tag, "ref": sha}, nil); err != nil {
		return nil, err
	}
	if p, err = findPipeline(ctx, c, s.parent.ID, q("ref", tag, "source", "push")); err != nil {
		return nil, err
	}
	add("tag", p, s.parent.ID)

	mrBranch := "rec/" + runID + "-mr"
	msha, err := commit(ctx, c, s.parent.ID, mrBranch, "main", 0, "rec: merge request", marker("mr"))
	if err != nil {
		return nil, err
	}
	if err := c.call(ctx, http.MethodPost, pp+"/merge_requests", map[string]any{
		"source_branch": mrBranch, "target_branch": "main", "title": "rec " + runID + ": same-project merge request", "remove_source_branch": true,
	}, nil); err != nil {
		return nil, err
	}
	if p, err = findPipeline(ctx, c, s.parent.ID, q("sha", msha, "source", "merge_request_event")); err != nil {
		return nil, err
	}
	add("mr", p, s.parent.ID)

	if err := startForkPipelines(ctx, c, s, runID, add); err != nil {
		return nil, err
	}

	before, err := latestPipelineID(ctx, c, s.parent.ID)
	if err != nil {
		return nil, err
	}
	if err := c.call(ctx, http.MethodPost, pp+"/pipeline_schedules/"+strconv.FormatInt(s.scheduleID, 10)+"/play", nil, nil); err != nil {
		return nil, err
	}
	for {
		if p, err = findPipeline(ctx, c, s.parent.ID, q("source", "schedule")); err != nil {
			return nil, err
		}
		if p.ID > before {
			break
		}
		if err := pause(ctx, 3*time.Second); err != nil {
			return nil, err
		}
	}
	add("schedule", p, s.parent.ID)

	if err := c.call(ctx, http.MethodPost, pp+"/pipeline", map[string]any{"ref": "main"}, &p); err != nil {
		return nil, err
	}
	add("api", p, s.parent.ID)
	return pipes, nil
}

// forkMR pushes a branch to the fork (from the parent's main, so that it carries this run's
// configuration) and opens a merge request from it into the parent, as the client's user.
func forkMR(ctx context.Context, c *client, s setup, branch, title string, marker []commitAction) (sha string, iid int64, err error) {
	if sha, err = commit(ctx, c, s.fork.ID, branch, "main", s.parent.ID, "rec: "+title, marker); err != nil {
		return "", 0, err
	}
	var mr struct {
		IID int64 `json:"iid"`
	}
	err = c.call(ctx, http.MethodPost, projectPath(s.fork.ID)+"/merge_requests", map[string]any{
		"source_branch": branch, "target_branch": "main", "target_project_id": s.parent.ID, "title": title,
	}, &mr)
	return sha, mr.IID, err
}

// startForkPipelines covers the three ways a merge request from a fork gets a pipeline:
//   - fork-in-fork: the partner user, who may not run pipelines in the parent, opens it; GitLab
//     runs the pipeline in the fork.
//   - fork-in-parent: a member of the parent then runs that merge request's pipeline in the parent.
//   - fork-by-parent-member: a member of the parent opens it; GitLab runs the pipeline in the parent
//     without being asked.
func startForkPipelines(ctx context.Context, c *client, s setup, runID string, add func(string, pipelineInfo, int64)) error {
	marker := func(name string) []commitAction {
		return []commitAction{{Action: "create", FilePath: "rec/" + runID + "-" + name + ".txt", Content: runID + "\n"}}
	}
	partner, revoke, err := impersonate(ctx, c, s.partnerUser)
	if err != nil {
		return fmt.Errorf("impersonating %s: %w", s.partnerUser.Username, err)
	}
	sha, iid, err := forkMR(ctx, partner, s, "rec/"+runID+"-fork", "rec "+runID+": merge request from the fork by a partner", marker("fork"))
	revoke()
	if err != nil {
		return err
	}
	p, err := findPipeline(ctx, c, s.fork.ID, q("sha", sha, "source", "merge_request_event"))
	if err != nil {
		return fmt.Errorf("the partner's merge request pipeline in the fork: %w", err)
	}
	add("fork-in-fork", p, s.fork.ID)

	var inParent pipelineInfo
	if err := c.call(ctx, http.MethodPost, projectPath(s.parent.ID)+"/merge_requests/"+strconv.FormatInt(iid, 10)+"/pipelines", nil, &inParent); err != nil {
		return fmt.Errorf("running the fork's merge request pipeline in the parent project: %w", err)
	}
	add("fork-in-parent", inParent, s.parent.ID)

	msha, _, err := forkMR(ctx, c, s, "rec/"+runID+"-fork-member", "rec "+runID+": merge request from the fork by a parent member", marker("fork-member"))
	if err != nil {
		return err
	}
	if p, err = findPipeline(ctx, c, s.parent.ID, q("sha", msha, "source", "merge_request_event")); err != nil {
		return fmt.Errorf("the parent member's merge request pipeline in the parent: %w", err)
	}
	add("fork-by-member", p, s.parent.ID)
	return nil
}

// closeMergeRequests closes the merge requests of this run (closing, not deleting: the pipelines
// and the history stay).
func closeMergeRequests(ctx context.Context, c *client, s setup, runID string) error {
	var mrs []struct {
		IID   int64  `json:"iid"`
		Title string `json:"title"`
	}
	if err := c.call(ctx, http.MethodGet, projectPath(s.parent.ID)+"/merge_requests?state=opened&per_page=100", nil, &mrs); err != nil {
		return err
	}
	for _, mr := range mrs {
		if !strings.HasPrefix(mr.Title, "rec "+runID+":") {
			continue
		}
		if err := c.call(ctx, http.MethodPut, projectPath(s.parent.ID)+"/merge_requests/"+strconv.FormatInt(mr.IID, 10), map[string]any{"state_event": "close"}, nil); err != nil {
			return err
		}
	}
	return nil
}

var finished = map[string]bool{"success": true, "failed": true, "canceled": true, "skipped": true, "manual": true}

func waitPipelines(ctx context.Context, c *client, pipes map[string]*pipe) error {
	for {
		open := 0
		for _, p := range pipes {
			if finished[p.status] {
				continue
			}
			var info pipelineInfo
			if err := c.call(ctx, http.MethodGet, projectPath(p.projectID)+"/pipelines/"+strconv.FormatInt(p.id, 10), nil, &info); err != nil {
				return err
			}
			p.status = info.Status
			if !finished[p.status] {
				open++
			}
		}
		if open == 0 {
			return nil
		}
		logf("%d pipelines still running", open)
		if err := pause(ctx, 10*time.Second); err != nil {
			return fmt.Errorf("waiting for the pipelines (do the instance runners pick up untagged jobs?): %w", err)
		}
	}
}

// jobLog is what one job printed.
type jobLog struct {
	pipeline  string
	projectID int64
	jobID     int64
	name      string
	status    string
	started   int64
	timeout   int64
	tokens    map[string]*tokenLog
	jobToken  string
}

type tokenLog struct {
	absent  bool
	dots    int
	header  string
	payload string
}

var (
	ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	line = regexp.MustCompile(`OIDCV\|([A-Za-z_]+)\|([a-z]+)\|(.*)$`)
)

func collect(ctx context.Context, c *client, pipes map[string]*pipe) (map[string]map[string]*jobLog, error) {
	out := map[string]map[string]*jobLog{}
	for key, p := range pipes {
		var jobs []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		base := projectPath(p.projectID)
		if err := c.call(ctx, http.MethodGet, base+"/pipelines/"+strconv.FormatInt(p.id, 10)+"/jobs?per_page=100", nil, &jobs); err != nil {
			return nil, err
		}
		out[key] = map[string]*jobLog{}
		for _, j := range jobs {
			trace, err := c.raw(ctx, base+"/jobs/"+strconv.FormatInt(j.ID, 10)+"/trace")
			if err != nil {
				return nil, err
			}
			jl, err := parseTrace(string(trace))
			if err != nil {
				return nil, fmt.Errorf("pipeline %s job %s: %w", key, j.Name, err)
			}
			jl.pipeline, jl.projectID, jl.jobID, jl.name, jl.status = key, p.projectID, j.ID, j.Name, j.Status
			out[key][j.Name] = jl
			logf("pipeline %-15s job %-24s %s, %d token variables", key, j.Name, j.Status, len(jl.tokens))
		}
	}
	return out, nil
}

func parseTrace(trace string) (*jobLog, error) {
	jl := &jobLog{tokens: map[string]*tokenLog{}}
	for _, raw := range strings.Split(trace, "\n") {
		l := strings.TrimRight(ansi.ReplaceAllString(raw, ""), "\r")
		if strings.Contains(l, "printf") { // the echo of a command, not its output
			continue
		}
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		name, kind, val := m[1], m[2], m[3]
		switch {
		case name == "job" && kind == "started":
			jl.started, _ = strconv.ParseInt(val, 10, 64)
		case name == "job" && kind == "timeout":
			jl.timeout, _ = strconv.ParseInt(val, 10, 64)
		case name == "CI_JOB_TOKEN" && kind == "shape":
			jl.jobToken = val
		default:
			t := jl.tokens[name]
			if t == nil {
				t = &tokenLog{}
				jl.tokens[name] = t
			}
			switch kind {
			case "absent":
				t.absent = true
			case "dots":
				t.dots, _ = strconv.Atoi(val)
			case "header":
				t.header = val
			case "payload":
				t.payload = val
			default:
				return nil, errors.New("unknown line kind " + kind)
			}
		}
	}
	return jl, nil
}
