package bastionmain

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

const (
	// contactInterval is the least time between two writes of one replica's contact row (R-BAS-010).
	contactInterval = time.Minute
	// contactRetention is how long a replica that does not report stays in the table: the pod names of old
	// rollouts (the comment of bastion_contact in 00032).
	contactRetention = 30 * 24 * time.Hour
	// pruneInterval is how often a replica runs the prune of old contacts.
	pruneInterval = time.Hour
	// maxTrackedReplicas bounds the memory gate; beyond it the gate forgets everything and the database
	// statement alone enforces the once-a-minute rule.
	maxTrackedReplicas = 4096
	// maxReplicasPerBastion bounds the contact rows of one registration: the names of the replicas are what
	// the Bastion reports.
	maxReplicasPerBastion = 256
	maxReleaseLen         = 64
	maxBaseURLLen         = 2048
	unknownReplica        = "unknown"
)

// Report is what a handshake tells the main installation about the replica that called.
type Report struct {
	// ClientAddress is the address the request came from after the trusted-proxy rule.
	ClientAddress string
	// UserAgent is the User-Agent of the request; "trove-bastion/<release>" carries the release.
	UserAgent string
	// ResponseBaseURL is the response base URL the main installation derived for the request.
	ResponseBaseURL string
}

// ContactOptions configure [NewContacts].
type ContactOptions struct {
	DB    meta.Runner
	Audit audit.Recorder
	Now   func() time.Time
}

// Contacts records the last contact of a Bastion replica (R-BAS-010).
type Contacts struct {
	o ContactOptions

	mu       sync.Mutex
	last     map[string]contactMark
	prunedAt time.Time
}

type contactMark struct {
	at time.Time
	fp string
}

// NewContacts returns the recorder.
func NewContacts(o ContactOptions) *Contacts {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Audit == nil {
		o.Audit = audit.Nop{}
	}
	return &Contacts{o: o, last: map[string]contactMark{}}
}

// Release extracts the release from the User-Agent of a Bastion ("trove-bastion/<release> …"), "" for any other.
func Release(userAgent string) string {
	rest, ok := strings.CutPrefix(userAgent, iface.UserAgentPrefix)
	if !ok {
		return ""
	}
	rel, _, _ := strings.Cut(rest, " ")
	return clip(rel, maxReleaseLen)
}

// Record stores the contact of the replica the recognition names, at most once a minute (R-BAS-010): the memory
// of this replica skips a repeat of unchanged values, and the statement itself writes a row only when it is a
// minute old or a value differs, so replicas that share a registration never write more often. It records
// bastion.contact (asynchronous) when the row is new or a reported value changed.
func (c *Contacts) Record(ctx context.Context, rec Recognition, rep Report) error {
	replica := rec.Replica
	if replica == "" {
		replica = unknownReplica
	}
	addr := ""
	if a, err := netip.ParseAddr(rep.ClientAddress); err == nil {
		addr = a.Unmap().WithZone("").String()
	}
	release := Release(rep.UserAgent)
	baseURL := clip(rep.ResponseBaseURL, maxBaseURLLen)
	fp := strings.Join([]string{addr, release, strconv.Itoa(rec.Interface), baseURL, rec.TokenID}, "\x00")
	key := rec.BastionID + "\x00" + replica
	now := c.o.Now()
	c.mu.Lock()
	if m, ok := c.last[key]; ok && m.fp == fp && now.Sub(m.at) < contactInterval {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	var row q.BastionContactUpsertRow
	err := c.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errNotPostgres
		}
		qs := q.New(ptx)
		var err error
		row, err = qs.BastionContactUpsert(ctx, q.BastionContactUpsertParams{
			ClientAddress: addr, Release: release, InterfaceVersion: int32(rec.Interface), //nolint:gosec // a small positive version
			BaseUrl: baseURL, BastionID: rec.BastionID, Replica: replica, TokenID: rec.TokenID,
		})
		if err != nil || row.Written == 0 {
			return err
		}
		// The replica name is the Bastion's to choose: keep the newest rows of the registration only.
		_, err = qs.BastionContactTrim(ctx, q.BastionContactTrimParams{BastionID: rec.BastionID, Keep: maxReplicasPerBastion})
		return err
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	if row.Written > 0 {
		// Only this replica's own write is remembered: a statement that wrote nothing found a row another
		// replica wrote within the minute, and the next call asks again.
		if len(c.last) >= maxTrackedReplicas {
			clear(c.last)
		}
		c.last[key] = contactMark{at: now, fp: fp}
	}
	prune := row.Written > 0 && now.Sub(c.prunedAt) >= pruneInterval
	if prune {
		c.prunedAt = now
	}
	c.mu.Unlock()
	if prune {
		_ = c.o.DB.InTx(ctx, func(tx meta.Tx) error { // best effort: the next write tries again
			ptx, ok := postgres.Unwrap(tx)
			if !ok {
				return errNotPostgres
			}
			_, err := q.New(ptx).BastionContactPrune(ctx, contactRetention.Seconds())
			return err
		})
	}
	if row.Written > 0 && row.Changed {
		c.o.Audit.Record(ctx, audit.Event{
			Type: audit.EventBastionContact, Outcome: audit.Success,
			Actor:   BastionActor(rec),
			Source:  audit.Source{ClientAddr: addr, UserAgent: rep.UserAgent},
			Subject: audit.Subject{Kind: "bastion", ID: rec.BastionID, Name: rec.Bastion},
			Details: mustJSON(map[string]any{
				"replica": replica, "release": release, "interfaceVersion": rec.Interface, "responseBaseUrl": baseURL, "clientAddress": addr,
			}),
		})
	}
	return nil
}

// BastionActor is the actor of an event a Bastion's call caused.
func BastionActor(rec Recognition) audit.Actor {
	return audit.Actor{Kind: "bastion", ID: rec.BastionID, Name: rec.Bastion, TokenID: rec.TokenID, AuthMethod: "bearer"}
}
