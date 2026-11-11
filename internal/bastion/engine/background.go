package engine

import (
	"context"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// refreshShape refreshes the shape answer a request used when it is in the last fifth of its period
// (R-BAS-020): one collapsed check in the background, so a caller in steady use never waits for the main
// installation to renew its answer. The token string outlives its request for the duration of that check only
// (H-447).
func (e *engine) refreshShape(q *rq, shape *answerVal) {
	if q.cred.anonymous && !e.anonymousReads() {
		return
	}
	period := e.cfg.Answers.Period
	if e.now().Sub(shape.ObtainedAt) < period/5*4 || !e.d.Link.Reachable() {
		return
	}
	key := "refresh|" + q.cred.key + "|" + q.repo
	c, leader := e.checks.join(key)
	if !leader {
		return
	}
	// The shape this request holds may be older than the answer a refresh that ended meanwhile stored: the
	// request read it before the refresh stored its result and reaches this point after the refresh left the
	// collapse. Only the current answer decides whether a check is due.
	if cur := e.answers.shape(context.Background(), q.cred.key, q.repo); cur != nil && e.now().Sub(cur.ObtainedAt) < period/5*4 {
		e.checks.finish(key, c, nil, nil)
		return
	}
	cred, repo, addr, client, class := q.cred, q.repo, q.addr, q.clientText, e.budgetClass(q)
	if !e.background(func() {
		defer e.checks.finish(key, c, nil, nil)
		ctx, cancel := context.WithTimeout(context.Background(), e.cfg.Main.ResponseHeaderTimeout+e.cfg.Main.QueueTimeout)
		defer cancel()
		req := iface.CheckRequest{ClientAddress: client, Repositories: []string{repo}}
		if !cred.anonymous {
			t := cred.token
			req.Credential = &t
		}
		sent := e.now()
		ans, err := e.d.Main.Check(ctx, class, req)
		if err != nil {
			return
		}
		e.recordCheck(ctx, cred, repo, addr, checkResult{answer: ans, sentAt: sent})
	}) {
		e.checks.finish(key, c, nil, nil)
	}
}

// recordCheck keeps what a check answered for a credential and a repository: the credential answer, the shape
// answer, or the invalid statement. It returns the shape and the subject kind.
func (e *engine) recordCheck(ctx context.Context, cred credential, repo, addr string, r checkResult) *answerVal {
	sub := r.answer.Subject
	switch sub.Kind {
	case iface.SubjectInvalid:
		if !cred.anonymous {
			inv := iface.Subject{Kind: iface.SubjectInvalid}
			e.answers.put(ctx, answers.Credential, cred.key, &answerVal{Answer: answerBase(answers.Credential, r.sentAt, inv, ""), Subject: inv})
			e.d.Guard.FailedAuth(ctx, addr)
		}
		return nil
	case iface.SubjectToken, iface.SubjectAnonymous:
	default:
		return nil
	}
	if !cred.anonymous {
		e.answers.put(ctx, answers.Credential, cred.key, &answerVal{Answer: answerBase(answers.Credential, r.sentAt, sub, ""), Subject: sub})
	}
	rr := r.answer.Repositories[repo]
	if !rr.Read.Valid() {
		rr = iface.RepositoryRead{Read: iface.ReadNone, Status: 404}
	}
	shape := &answerVal{Answer: answerBase(answers.Shape, r.sentAt, sub, repo), Read: rr.Read, Status: rr.Status}
	e.answers.put(ctx, answers.Shape, shapeKey(cred.key, repo), shape)
	return shape
}
