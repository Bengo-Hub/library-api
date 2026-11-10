// Package members keeps library members linked to the MarketFlow CRM, which owns customer
// identity. A member stores its crm_contact_id; display_name, contact_phone and contact_email
// are a read-only snapshot of the CRM contact, written when the member is linked and refreshed
// when the CRM publishes crm.contact.updated.
package members

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/Bengo-Hub/httpware/contact"
	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/Bengo-Hub/shared-service-client/crm"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/member"
	"github.com/bengobox/library-service/internal/ent/predicate"
)

const (
	// ReconcileLockID is the Postgres advisory lock that keeps one reconcile run fleet wide.
	ReconcileLockID = 727271051
	// ReconcileEvery is how often the reconciler links members that are still unlinked.
	ReconcileEvery = 10 * time.Minute
	// MetaCRMUnlinkedAt is the member metadata key set when the CRM deleted the linked
	// contact. The reconciler never re-links such a member; a staff edit clears the key.
	MetaCRMUnlinkedAt = "crm_unlinked_at"
	// reconcileBatch is the most members one reconcile run tries to link.
	reconcileBatch = 500
	// linkBudget bounds one CRM call made while a person waits.
	linkBudget = 2 * time.Second
	// maxTracked caps each in-memory id set (refused members, pending pushes).
	maxTracked = 10000

	subjectContactUpdated = "crm.contact.updated"
	durableContactUpdated = "library-contact-updated"
	subjectContactDeleted = "crm.contact.deleted"
	durableContactDeleted = "library-contact-deleted"
)

// Identity is a member's name, phone and email as they should be stored. CrmContactID is nil
// when the member could not be linked (no phone or email, or the CRM was unavailable).
type Identity struct {
	CrmContactID *uuid.UUID
	Name         string
	Phone        string
	Email        string
}

// RejectedError is a CRM refusal of the data itself (400, 404, 409 or 422, for example an
// invalid or already used phone). Handlers answer 400 with its message so the person can
// correct the form.
type RejectedError struct {
	Status  int
	Message string
}

func (e *RejectedError) Error() string { return e.Message }

// outcome says what happened to one link attempt.
type outcome int

const (
	linked      outcome = iota // the CRM answered with a contact
	noContact                  // nothing to link: no phone or email, or the CRM is not configured
	unavailable                // the CRM could not be reached or failed; try again later
	rejected                   // the CRM refused the data
)

// Syncer links members to the CRM, reconciles unlinked members and refreshes snapshots.
type Syncer struct {
	db    *ent.Client
	sqlDB *sql.DB
	crm   *crm.Client
	log   *zap.Logger

	mu       sync.Mutex
	refused  map[uuid.UUID]struct{}
	pendPush map[uuid.UUID]struct{}
}

// NewSyncer builds a Syncer. sqlDB is the pool behind db and is used for the advisory lock.
// A nil or unconfigured crm client makes every link a no-op that still saves the member.
func NewSyncer(db *ent.Client, sqlDB *sql.DB, c *crm.Client, log *zap.Logger) *Syncer {
	if log == nil {
		log = zap.NewNop()
	}
	return &Syncer{
		db: db, sqlDB: sqlDB, crm: c, log: log.Named("members.crm"),
		refused: map[uuid.UUID]struct{}{}, pendPush: map[uuid.UUID]struct{}{},
	}
}

// Normalise returns the phone in E.164 when it is valid (else trimmed) and the email trimmed
// and lower-cased when it is valid (else trimmed).
func Normalise(phone, email string) (string, string) {
	phone = strings.TrimSpace(phone)
	if n, err := contact.NormalizePhone(phone, contact.DefaultRegion); err == nil {
		phone = n
	}
	email = strings.TrimSpace(email)
	if n, err := contact.NormalizeEmail(email); err == nil {
		email = n
	}
	return phone, email
}

// Link creates or finds the CRM contact for a new member, sending phone and email. It returns
// the identity to store: the CRM's values when linked, the normalised input otherwise. Only a
// CRM refusal of the data is an error (*RejectedError); an outage still lets the member be
// saved, and the reconciler links it later.
func (s *Syncer) Link(ctx context.Context, tenantID uuid.UUID, name, phone, email string) (Identity, error) {
	id, _, err := s.link(ctx, tenantID, name, phone, email)
	return id, err
}

func (s *Syncer) link(ctx context.Context, tenantID uuid.UUID, name, phone, email string) (Identity, outcome, error) {
	phone, email = Normalise(phone, email)
	out := Identity{Name: strings.TrimSpace(name), Phone: phone, Email: email}
	if s == nil || (phone == "" && email == "") {
		return out, noContact, nil
	}
	first, last := crm.SplitName(out.Name)
	cctx, cancel := context.WithTimeout(ctx, linkBudget)
	defer cancel()
	got, err := s.crm.UpsertStrict(cctx, crm.UpsertInput{
		TenantID: tenantID, Phone: phone, Email: email, FirstName: first, LastName: last,
	})
	if err != nil {
		oc, rerr := s.classify(err)
		return out, oc, rerr
	}
	return fromContact(got, out), linked, nil
}

// Save applies an edit to a member's identity. A linked member is edited in the CRM first
// (only the non-empty values are sent); an unlinked member, or one whose contact is gone, is
// linked as on create. The returned identity is what the snapshot should hold. When the CRM
// is unavailable for a linked member, the edit is kept locally and queued so the reconciler
// pushes it to the CRM later.
func (s *Syncer) Save(ctx context.Context, tenantID, memberID uuid.UUID, crmID *uuid.UUID, name, phone, email string) (Identity, error) {
	if crmID == nil || *crmID == uuid.Nil {
		return s.Link(ctx, tenantID, name, phone, email)
	}
	phone, email = Normalise(phone, email)
	out := Identity{CrmContactID: crmID, Name: strings.TrimSpace(name), Phone: phone, Email: email}
	if s == nil {
		return out, nil
	}
	got, err := s.update(ctx, tenantID, *crmID, out)
	if errors.Is(err, crm.ErrNotFound) {
		return s.Link(ctx, tenantID, name, phone, email)
	}
	if err != nil {
		oc, rerr := s.classify(err)
		if oc == unavailable {
			s.log.Warn("crm unavailable, member edit saved locally and queued for push",
				zap.String("member_id", memberID.String()), zap.String("crm_contact_id", crmID.String()))
			s.track(s.pendPush, memberID)
		}
		return out, rerr
	}
	s.untrack(s.pendPush, memberID)
	return fromContact(got, out), nil
}

// update sends the non-empty identity values to the CRM contact.
func (s *Syncer) update(ctx context.Context, tenantID, crmID uuid.UUID, id Identity) (crm.Contact, error) {
	var in crm.UpdateInput
	if id.Name != "" {
		first, last := crm.SplitName(id.Name)
		in.FirstName, in.LastName = &first, &last
	}
	if id.Phone != "" {
		p := id.Phone
		in.Phone = &p
	}
	if id.Email != "" {
		e := id.Email
		in.Email = &e
	}
	cctx, cancel := context.WithTimeout(ctx, linkBudget)
	defer cancel()
	return s.crm.Update(cctx, tenantID, crmID, in)
}

// classify maps a CRM error to an outcome. Only a refusal of the data (400, 404, 409, 422) is
// returned as an error. A bad service key (401, 403), a timeout or rate limit (408, 429), a
// 5xx or no answer at all is the CRM being unavailable, not the person's fault.
func (s *Syncer) classify(err error) (outcome, error) {
	if errors.Is(err, crm.ErrDisabled) || errors.Is(err, crm.ErrNoIdentifier) || errors.Is(err, crm.ErrNoTenant) {
		return noContact, nil
	}
	var ce *crm.Error
	if errors.As(err, &ce) {
		switch ce.Status {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
			msg := ce.Message
			if msg == "" {
				msg = "the CRM rejected this contact"
			}
			return rejected, &RejectedError{Status: ce.Status, Message: msg}
		}
	}
	s.log.Warn("crm unavailable, member saved without crm update", zap.Error(err))
	return unavailable, nil
}

// fromContact builds the snapshot from the CRM's answer, keeping the input where the CRM has
// no value.
func fromContact(c crm.Contact, fallback Identity) Identity {
	out := fallback
	if c.ID != uuid.Nil {
		id := c.ID
		out.CrmContactID = &id
	}
	if n := strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName)); n != "" {
		out.Name = n
	}
	if c.Phone != "" {
		out.Phone = c.Phone
	}
	if c.Email != "" {
		out.Email = c.Email
	}
	return out
}

// Apply writes a linked identity onto a member row.
func (s *Syncer) Apply(ctx context.Context, memberID uuid.UUID, id Identity) error {
	if s == nil || id.CrmContactID == nil {
		return nil
	}
	u := s.db.Member.UpdateOneID(memberID).SetCrmContactID(*id.CrmContactID).
		SetContactPhone(id.Phone).SetContactEmail(id.Email)
	if id.Name != "" {
		u.SetDisplayName(id.Name)
	}
	return u.Exec(ctx)
}

// ClearUnlinked removes the crm_unlinked_at flag from member metadata. It returns the map to
// store and whether anything changed. Staff editing a member's identity calls it, so the
// member may be linked again.
func ClearUnlinked(md map[string]any) (map[string]any, bool) {
	if _, ok := md[MetaCRMUnlinkedAt]; !ok {
		return md, false
	}
	out := make(map[string]any, len(md))
	for k, v := range md {
		if k != MetaCRMUnlinkedAt {
			out[k] = v
		}
	}
	return out, true
}

// ImportLinker links the rows of one bulk import. After the first CRM outage it stops calling
// the CRM for the rest of the import, so a 5000 row file does not wait 2s per row; the
// reconciler links those members later.
type ImportLinker struct {
	s    *Syncer
	down bool
}

// NewImportLinker starts a linker for one import job.
func (s *Syncer) NewImportLinker() *ImportLinker { return &ImportLinker{s: s} }

// Link behaves like Syncer.Link, but skips the CRM once it has been unavailable.
func (l *ImportLinker) Link(ctx context.Context, tenantID uuid.UUID, name, phone, email string) (Identity, error) {
	if l.down {
		p, e := Normalise(phone, email)
		return Identity{Name: strings.TrimSpace(name), Phone: p, Email: e}, nil
	}
	id, oc, err := l.s.link(ctx, tenantID, name, phone, email)
	if oc == unavailable {
		l.down = true
	}
	return id, err
}

// notUnlinkedByCRM matches members without the crm_unlinked_at metadata flag.
func notUnlinkedByCRM() predicate.Member {
	return member.Or(
		member.MetadataIsNil(),
		member.Not(predicate.Member(func(s *entsql.Selector) {
			s.Where(sqljson.HasKey(s.C(member.FieldMetadata), sqljson.Path(MetaCRMUnlinkedAt)))
		})),
	)
}

// ReconcileOnce first pushes the edits this pod saved while the CRM was down, then links up to
// 500 unlinked members that have a phone or email, oldest first, under the advisory lock. It
// returns how many members it pushed or linked. A run stops early when the CRM is unavailable.
// Members the CRM refused, and members whose contact the CRM deleted, are not linked.
func (s *Syncer) ReconcileOnce(ctx context.Context) (int, error) {
	if s == nil || s.sqlDB == nil {
		return 0, nil
	}
	n, down := s.pushPending(ctx)
	if down {
		return n, nil
	}

	conn, err := s.sqlDB.Conn(ctx)
	if err != nil {
		return n, fmt.Errorf("reconcile: conn: %w", err)
	}
	defer conn.Close()
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, int64(ReconcileLockID)).Scan(&got); err != nil {
		return n, fmt.Errorf("reconcile: lock: %w", err)
	}
	if !got {
		return n, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(ReconcileLockID))
	}()

	q := s.db.Member.Query().Where(
		member.CrmContactIDIsNil(),
		member.Or(member.ContactPhoneNEQ(""), member.ContactEmailNEQ("")),
		notUnlinkedByCRM(),
	)
	if skip := s.ids(s.refused); len(skip) > 0 {
		q = q.Where(member.IDNotIn(skip...))
	}
	rows, err := q.Order(ent.Asc(member.FieldCreatedAt)).Limit(reconcileBatch).All(ctx)
	if err != nil {
		return n, fmt.Errorf("reconcile: load members: %w", err)
	}
	for _, m := range rows {
		id, oc, lerr := s.link(ctx, m.TenantID, m.DisplayName, m.ContactPhone, m.ContactEmail)
		switch oc {
		case unavailable:
			return n, nil
		case rejected:
			s.track(s.refused, m.ID)
			s.log.Info("crm refused member during reconcile", zap.String("member_id", m.ID.String()), zap.Error(lerr))
			continue
		case noContact:
			continue
		}
		if err := s.Apply(ctx, m.ID, id); err != nil {
			s.log.Warn("reconcile: save link failed", zap.String("member_id", m.ID.String()), zap.Error(err))
			continue
		}
		n++
	}
	return n, nil
}

// pushPending sends the local snapshot of each queued member to its CRM contact. It reports
// how many it pushed and whether the CRM was unavailable. The queue lives in this pod's memory
// only, so it needs no fleet lock, and a restart drops it.
func (s *Syncer) pushPending(ctx context.Context) (int, bool) {
	n := 0
	for _, id := range s.ids(s.pendPush) {
		m, err := s.db.Member.Get(ctx, id)
		if err != nil || m.CrmContactID == nil {
			s.untrack(s.pendPush, id)
			continue
		}
		snap := Identity{CrmContactID: m.CrmContactID, Name: m.DisplayName, Phone: m.ContactPhone, Email: m.ContactEmail}
		got, uerr := s.update(ctx, m.TenantID, *m.CrmContactID, snap)
		if uerr != nil {
			if errors.Is(uerr, crm.ErrNotFound) {
				s.untrack(s.pendPush, id)
				continue
			}
			oc, _ := s.classify(uerr)
			if oc == unavailable {
				return n, true
			}
			s.untrack(s.pendPush, id)
			if oc == rejected {
				s.log.Info("crm refused a queued member edit", zap.String("member_id", id.String()),
					zap.String("crm_contact_id", m.CrmContactID.String()))
			}
			continue
		}
		s.untrack(s.pendPush, id)
		if err := s.Apply(ctx, id, fromContact(got, snap)); err != nil {
			s.log.Warn("reconcile: save pushed member failed", zap.String("member_id", id.String()), zap.Error(err))
			continue
		}
		n++
	}
	return n, false
}

// RunReconciler runs ReconcileOnce every interval until ctx ends.
func (s *Syncer) RunReconciler(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.ReconcileOnce(ctx); err != nil {
			s.log.Warn("member crm reconcile failed", zap.Error(err))
		} else if n > 0 {
			s.log.Info("member crm reconcile done", zap.Int("members", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) track(set map[uuid.UUID]struct{}, id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(set) < maxTracked {
		set[id] = struct{}{}
	}
}

func (s *Syncer) untrack(set map[uuid.UUID]struct{}, id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(set, id)
}

func (s *Syncer) ids(set map[uuid.UUID]struct{}) []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]uuid.UUID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// RefreshContacts rewrites the snapshot of every member linked to one of ids from the CRM.
// It returns how many members it updated.
func (s *Syncer) RefreshContacts(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := s.db.Member.Query().Where(member.TenantID(tenantID), member.CrmContactIDIn(ids...)).All(ctx)
	if err != nil {
		return 0, fmt.Errorf("refresh: load members: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	contacts, err := s.crm.BatchGet(ctx, tenantID, ids)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range rows {
		c, ok := contacts[*m.CrmContactID]
		if !ok {
			continue
		}
		u := s.db.Member.UpdateOneID(m.ID).SetContactPhone(c.Phone).SetContactEmail(c.Email)
		if name := strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName)); name != "" {
			u.SetDisplayName(name)
		}
		if err := u.Exec(ctx); err != nil {
			return n, fmt.Errorf("refresh: save member %s: %w", m.ID, err)
		}
		s.untrack(s.pendPush, m.ID)
		n++
	}
	return n, nil
}

// identityFields are the contact fields the member snapshot holds.
var identityFields = map[string]bool{"first_name": true, "last_name": true, "phone": true, "email": true, "name": true}

// contactEvent reads the tenant and contact id of a crm.contact.* event. ok is false when the
// event cannot be used (it is then acknowledged and dropped).
func (s *Syncer) contactEvent(subject string, data []byte) (evt *sharedevents.Event, contactID uuid.UUID, ok bool) {
	evt, err := sharedevents.FromJSON(data)
	if err != nil {
		s.log.Warn(subject+": unreadable event, skipped", zap.Error(err))
		return nil, uuid.Nil, false
	}
	contactID, _ = uuid.Parse(fmt.Sprint(evt.Payload["contact_id"]))
	if evt.TenantID == uuid.Nil || contactID == uuid.Nil {
		return nil, uuid.Nil, false
	}
	return evt, contactID, true
}

// HandleContactUpdated processes one crm.contact.updated event ({"contact_id","fields"} with
// the contact's tenant on the envelope). An error means the event should be redelivered.
func (s *Syncer) HandleContactUpdated(ctx context.Context, data []byte) error {
	evt, contactID, ok := s.contactEvent(subjectContactUpdated, data)
	if !ok {
		return nil
	}
	if raw, ok := evt.Payload["fields"].([]any); ok && len(raw) > 0 {
		relevant := false
		for _, f := range raw {
			if name, ok := f.(string); ok && identityFields[name] {
				relevant = true
				break
			}
		}
		if !relevant {
			return nil
		}
	}
	_, err := s.RefreshContacts(ctx, evt.TenantID, []uuid.UUID{contactID})
	if errors.Is(err, crm.ErrDisabled) {
		return nil
	}
	return err
}

// HandleContactDeleted processes one crm.contact.deleted event: members of the event's tenant
// linked to that contact lose the link and get the crm_unlinked_at flag, so the reconciler
// does not create the contact again. Members, loans and fines are kept.
func (s *Syncer) HandleContactDeleted(ctx context.Context, data []byte) error {
	evt, contactID, ok := s.contactEvent(subjectContactDeleted, data)
	if !ok {
		return nil
	}
	rows, err := s.db.Member.Query().Where(member.TenantID(evt.TenantID), member.CrmContactID(contactID)).All(ctx)
	if err != nil {
		return fmt.Errorf("contact deleted: load members: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, m := range rows {
		md := make(map[string]any, len(m.Metadata)+1)
		for k, v := range m.Metadata {
			md[k] = v
		}
		md[MetaCRMUnlinkedAt] = now
		if err := s.db.Member.UpdateOneID(m.ID).ClearCrmContactID().SetMetadata(md).Exec(ctx); err != nil {
			return fmt.Errorf("contact deleted: unlink member %s: %w", m.ID, err)
		}
		s.untrack(s.pendPush, m.ID)
	}
	return nil
}

// Subscribe binds the durable JetStream consumers for crm.contact.updated and
// crm.contact.deleted. It binds to whichever stream carries crm.> (it is not named "crm" in
// production) and creates "crm" only when none does, the same rule as pos-api's CRM
// subscribers.
func (s *Syncer) Subscribe(nc *nats.Conn) error {
	if nc == nil {
		return fmt.Errorf("crm contact subscriber: NATS connection is nil")
	}
	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("crm contact subscriber: jetstream init: %w", err)
	}
	stream, err := crmStream(js, subjectContactUpdated)
	if err != nil {
		return err
	}
	s.bind(js, stream, subjectContactUpdated, durableContactUpdated, s.HandleContactUpdated)
	s.bind(js, stream, subjectContactDeleted, durableContactDeleted, s.HandleContactDeleted)
	s.log.Info("crm.contact.updated and crm.contact.deleted subscriptions registered")
	return nil
}

func crmStream(js nats.JetStreamContext, subject string) (string, error) {
	stream, err := js.StreamNameBySubject(subject)
	if err == nil && stream != "" {
		return stream, nil
	}
	if _, addErr := js.AddStream(&nats.StreamConfig{
		Name:      "crm",
		Subjects:  []string{"crm.>"},
		Retention: nats.LimitsPolicy,
		MaxAge:    72 * time.Hour,
		Storage:   nats.FileStorage,
	}); addErr != nil && !errors.Is(addErr, nats.ErrStreamNameAlreadyInUse) {
		name, lookupErr := js.StreamNameBySubject(subject)
		if lookupErr != nil || name == "" {
			return "", fmt.Errorf("crm contact subscriber: ensure crm stream: %w", addErr)
		}
		return name, nil
	}
	return "crm", nil
}

func (s *Syncer) bind(js nats.JetStreamContext, stream, subject, durable string, handle func(context.Context, []byte) error) {
	sharedevents.SubscribeQueueWithRebind(s.log, js, stream, subject, durable,
		func(msg *nats.Msg) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := handle(ctx, msg.Data); err != nil {
				s.log.Warn(subject+": handling failed, will retry", zap.Error(err))
				_ = msg.Nak()
				return
			}
			_ = msg.Ack()
		},
		nats.Durable(durable),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(5),
		nats.DeliverNew(),
	)
}
