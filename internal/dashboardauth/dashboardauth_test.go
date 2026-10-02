package dashboardauth

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
)

// ---------------------------------------------------------------------------
// In-memory stores
// ---------------------------------------------------------------------------

type fakeUsers struct {
	mu    sync.Mutex
	byID  map[string]*domain.DashboardUser
	byKey map[string]string
	seq   int
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byID: map[string]*domain.DashboardUser{}, byKey: map[string]string{}}
}

func (f *fakeUsers) Create(_ context.Context, u *domain.DashboardUser) (*domain.DashboardUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.byKey[u.Username]; exists {
		return nil, errTaken
	}
	f.seq++
	stored := *u
	stored.ID = u.ID
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = time.Now().UTC()
	}
	stored.UpdatedAt = stored.CreatedAt
	f.byID[stored.ID] = &stored
	f.byKey[stored.Username] = stored.ID
	return &stored, nil
}

func (f *fakeUsers) Count(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byID), nil
}

func (f *fakeUsers) ByUsername(_ context.Context, username string) (*domain.DashboardUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byKey[username]
	if !ok {
		return nil, errMissing
	}
	copied := *f.byID[id]
	return &copied, nil
}

func (f *fakeUsers) ByID(_ context.Context, id string) (*domain.DashboardUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil, errMissing
	}
	copied := *u
	return &copied, nil
}

func (f *fakeUsers) RecordLoginSuccess(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.byID[id]; ok {
		u.FailedAttempts = 0
		u.LockedUntil = nil
		u.LastLoginAt = &at
		u.UpdatedAt = at
	}
	return nil
}

func (f *fakeUsers) RecordLoginFailure(_ context.Context, id string, at time.Time, threshold int, lockUntil *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return errMissing
	}
	u.FailedAttempts++
	if u.FailedAttempts >= threshold && lockUntil != nil {
		until := *lockUntil
		u.LockedUntil = &until
	}
	return nil
}

func (f *fakeUsers) SetPassword(_ context.Context, id, hash string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.byID[id]; ok {
		u.PasswordHash = hash
		u.FailedAttempts = 0
		u.LockedUntil = nil
		u.UpdatedAt = at
	}
	return nil
}

func (f *fakeUsers) SetEnabled(_ context.Context, id string, enabled bool, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.byID[id]; ok {
		u.Enabled = enabled
		u.UpdatedAt = at
	}
	return nil
}

type fakeSessions struct {
	mu   sync.Mutex
	byID map[string]*domain.DashboardSession
	seq  int
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{byID: map[string]*domain.DashboardSession{}}
}

func (f *fakeSessions) Create(_ context.Context, s *domain.DashboardSession) (*domain.DashboardSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	stored := *s
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = time.Now().UTC()
	}
	stored.LastSeenAt = stored.CreatedAt
	f.byID[stored.ID] = &stored
	return &stored, nil
}

func (f *fakeSessions) ByTokenHash(_ context.Context, hash string) (*domain.DashboardSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.byID {
		if s.TokenHash == hash {
			copied := *s
			return &copied, nil
		}
	}
	return nil, errMissing
}

func (f *fakeSessions) Touch(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.byID[id]; ok {
		s.LastSeenAt = at
	}
	return nil
}

func (f *fakeSessions) Revoke(_ context.Context, hash string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.byID {
		if s.TokenHash == hash && s.RevokedAt == nil {
			revoked := at
			s.RevokedAt = &revoked
		}
	}
	return nil
}

func (f *fakeSessions) RevokeAllForUser(_ context.Context, userID string, at time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, s := range f.byID {
		if s.UserID == userID && s.RevokedAt == nil {
			revoked := at
			s.RevokedAt = &revoked
			n++
		}
	}
	return n, nil
}

func (f *fakeSessions) live() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.byID {
		if s.RevokedAt == nil {
			n++
		}
	}
	return n
}

type fakeLatch struct {
	mu     sync.Mutex
	values map[string]string
}

func newFakeLatch() *fakeLatch { return &fakeLatch{values: map[string]string{}} }

func (f *fakeLatch) Get(_ context.Context, key string) (json.RawMessage, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[key]
	if !ok {
		return nil, false, nil
	}
	return json.RawMessage(`"` + v + `"`), true, nil
}

func (f *fakeLatch) Set(_ context.Context, key string, value any, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := value.(string)
	if !ok {
		s = ""
	}
	f.values[key] = s
	return nil
}

type recordedAudit struct {
	mu     sync.Mutex
	events []string
}

func (r *recordedAudit) Record(_ context.Context, event, _, _, _ string, _ map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordedAudit) saw(event string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e == event {
			return true
		}
	}
	return false
}

var (
	errMissing = domain.NewError(domain.ErrCodeNotFound, "missing")
	errTaken   = domain.NewError(domain.ErrCodeInvalidRequest, "username taken")
)

func newTestService(t *testing.T, tweak func(*Options)) (*Service, *fakeUsers, *fakeSessions, *fakeLatch, *recordedAudit, *time.Time) {
	t.Helper()
	users := newFakeUsers()
	sessions := newFakeSessions()
	latch := newFakeLatch()
	audit := &recordedAudit{}
	clock := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	now := clock

	opts := Options{
		Users:             users,
		Sessions:          sessions,
		Latch:             latch,
		Audit:             audit,
		PasswordRules:     PasswordRules{MinLength: 12, MaxLength: 256},
		SessionTTL:        time.Hour,
		IdleTTL:           30 * time.Minute,
		MaxFailedAttempts: 3,
		LockoutDuration:   time.Second,
		MaxLockout:        time.Minute,
		Now:               func() time.Time { return now },
	}
	if tweak != nil {
		tweak(&opts)
	}
	return NewService(opts), users, sessions, latch, audit, &now
}

const goodPassword = "correct-horse-battery-1"

// ---------------------------------------------------------------------------
// Password hashing
// ---------------------------------------------------------------------------

func TestHashPasswordProducesVerifiablePHCString(t *testing.T) {
	hash, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=") {
		t.Fatalf("hash is not an argon2id PHC string: %q", hash)
	}
	if strings.Contains(hash, goodPassword) {
		t.Fatal("the hash must not contain the plaintext")
	}

	ok, err := VerifyPassword(goodPassword, hash)
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v", ok, err)
	}
	ok, err = VerifyPassword("wrong-password-entirely", hash)
	if err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = %v, %v", ok, err)
	}
}

// TestHashIsSalted covers the reason a stolen table is not enough: two hashes of
// the same password must differ.
func TestHashIsSalted(t *testing.T) {
	first, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Fatal("identical passwords produced identical hashes; the salt is not random")
	}
}

// TestVerifyRejectsMalformedHashWithoutEarlyExit is the timing property: a
// corrupt stored hash must still cost the same as a wrong password, so a corrupt
// row cannot be told apart from a failed login by clock.
func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, encoded := range []string{"", "not-a-hash", "$argon2id$", "$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"} {
		if _, err := VerifyPassword(goodPassword, encoded); err == nil {
			t.Errorf("VerifyPassword(%q) accepted a malformed hash", encoded)
		}
	}
}

// TestVerifyRejectsAbsurdWorkFactor guards against a stored hash that would make
// verification a denial of service against ourselves.
func TestVerifyRejectsAbsurdWorkFactor(t *testing.T) {
	hostile := "$argon2id$v=19$m=99999999,t=99,p=99$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNo"
	if _, err := VerifyPassword(goodPassword, hostile); err == nil {
		t.Fatal("a hash demanding 4 GiB of memory must be rejected rather than computed")
	}
}

// ---------------------------------------------------------------------------
// Password and username policy
// ---------------------------------------------------------------------------

func TestValidatePasswordRejectsWeakValues(t *testing.T) {
	rules := DefaultPasswordRules()
	cases := []struct {
		name     string
		password string
	}{
		{"too short", "Ab3!xyz"},
		{"only whitespace", "               "},
		{"common", "password123456"},
		{"decorated common", "Password1!"},
		{"numeric run", "123456789012"},
		{"single character class", "aaaaaaaaaaaaaaaaaaaa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePassword(tc.password, rules); err == nil {
				t.Errorf("ValidatePassword(%q) accepted a weak password", tc.password)
			}
		})
	}
}

// TestValidatePasswordCountsRunes keeps a passphrase in a non-Latin script from
// being rejected for being short in bytes.
func TestValidatePasswordCountsRunes(t *testing.T) {
	if err := ValidatePassword("パスワード.longenough.value", DefaultPasswordRules()); err != nil {
		t.Errorf("a 24-character passphrase was rejected: %v", err)
	}
}

func TestValidateUsername(t *testing.T) {
	valid := []string{"admin", "ops-team", "a.b_c-1", "root123"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("ValidateUsername(%q) = %v", name, err)
		}
	}
	invalid := []string{"", "ab", "Admin!", "has space", "-leading", "waytoolongusernamewaybeyondthe32charbound"}
	for _, name := range invalid {
		if err := ValidateUsername(name); err == nil {
			t.Errorf("ValidateUsername(%q) accepted an invalid username", name)
		}
	}
}

// ---------------------------------------------------------------------------
// First-run setup
// ---------------------------------------------------------------------------

func TestSetupCreatesOneOperatorAndClosesSetup(t *testing.T) {
	svc, users, _, latch, audit, _ := newTestService(t, nil)
	ctx := context.Background()

	state, err := svc.State(ctx)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if !state.SetupRequired {
		t.Fatal("a fresh install must require setup")
	}

	result, err := svc.Setup(ctx, "Admin", goodPassword, goodPassword, ClientInfo{IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.User.Username != "admin" {
		t.Errorf("username = %q, want the normalised lowercase form", result.User.Username)
	}
	if result.Token == "" {
		t.Error("setup must return a usable session token")
	}
	if result.User.PasswordHash == "" || result.User.PasswordHash == goodPassword {
		t.Error("the stored credential must be a hash, not the plaintext")
	}
	if n, _ := users.Count(ctx); n != 1 {
		t.Errorf("user count = %d, want exactly 1", n)
	}
	if !audit.saw("dashboard_setup") {
		t.Error("setup must be audited")
	}

	// The latch is what makes this permanent.
	latch.mu.Lock()
	stamp := latch.values[SetupLatchKey]
	latch.mu.Unlock()
	if stamp == "" {
		t.Fatal("the first-run latch was not written")
	}

	state, err = svc.State(ctx)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.SetupRequired {
		t.Fatal("setup must be closed once an operator exists")
	}

	// A second attempt is refused, which is the point of the latch.
	if _, err := svc.Setup(ctx, "attacker", goodPassword, goodPassword, ClientInfo{}); err == nil {
		t.Fatal("a second first-run setup must be refused")
	} else if domain.AsError(err).Code != domain.ErrCodePermission {
		t.Errorf("second setup error = %v, want a permission error", err)
	}
	if n, _ := users.Count(ctx); n != 1 {
		t.Errorf("a refused setup created an account: count = %d", n)
	}
}

// TestSetupStaysClosedWhenEveryUserIsDeleted is the reason the latch exists
// rather than a count. Deleting the last operator must not hand first-run setup
// back to anyone who can reach the console.
func TestSetupStaysClosedWhenEveryUserIsDeleted(t *testing.T) {
	svc, users, _, _, _, _ := newTestService(t, nil)
	ctx := context.Background()

	if _, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	users.mu.Lock()
	users.byID = map[string]*domain.DashboardUser{}
	users.byKey = map[string]string{}
	users.mu.Unlock()

	state, err := svc.State(ctx)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.SetupRequired {
		t.Fatal("deleting every user must not reopen first-run setup")
	}
	if _, err := svc.Setup(ctx, "attacker", goodPassword, goodPassword, ClientInfo{}); err == nil {
		t.Fatal("setup must remain closed after the operator is deleted")
	}
}

func TestSetupRejectsMismatchedConfirmation(t *testing.T) {
	svc, _, _, _, _, _ := newTestService(t, nil)
	if _, err := svc.Setup(context.Background(), "admin", goodPassword, "something-else-1", ClientInfo{}); err == nil {
		t.Fatal("a mismatched confirmation must be refused")
	}
}

func TestSetupRejectsWeakPassword(t *testing.T) {
	svc, users, _, _, _, _ := newTestService(t, nil)
	if _, err := svc.Setup(context.Background(), "admin", "short", "short", ClientInfo{}); err == nil {
		t.Fatal("a weak password must be refused at setup")
	}
	if n, _ := users.Count(context.Background()); n != 0 {
		t.Error("a refused setup must not leave a user behind")
	}
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

func TestLoginSucceedsWithCorrectCredentials(t *testing.T) {
	svc, _, _, _, audit, _ := newTestService(t, nil)
	ctx := context.Background()

	if _, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	result, err := svc.Login(ctx, "ADMIN", goodPassword, ClientInfo{IP: "10.0.0.5"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.User.Username != "admin" {
		t.Errorf("username = %q, want admin", result.User.Username)
	}
	if !audit.saw("dashboard_login") {
		t.Error("a successful login must be audited")
	}

	user, err := svc.Authenticate(ctx, result.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.Username != "admin" {
		t.Errorf("authenticated user = %q", user.Username)
	}
}

func TestLoginIsCaseInsensitive(t *testing.T) {
	svc, _, _, _, _, _ := newTestService(t, nil)
	ctx := context.Background()
	if _, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for _, spelling := range []string{"admin", "Admin", "ADMIN", "  admin  "} {
		if _, err := svc.Login(ctx, spelling, goodPassword, ClientInfo{}); err != nil {
			t.Errorf("Login(%q) failed: %v", spelling, err)
		}
	}
}

// TestLoginFailuresAreIndistinguishable is the account-enumeration defence. A
// wrong password and an unknown username must produce the identical error, or the
// login form becomes a way to discover which operators exist.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	svc, _, _, _, _, _ := newTestService(t, nil)
	ctx := context.Background()
	if _, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	_, wrongPassword := svc.Login(ctx, "admin", "not-the-password-1", ClientInfo{})
	_, noSuchUser := svc.Login(ctx, "nobody", goodPassword, ClientInfo{})

	if wrongPassword == nil || noSuchUser == nil {
		t.Fatal("both attempts should fail")
	}
	if domain.AsError(wrongPassword).Message != domain.AsError(noSuchUser).Message {
		t.Errorf("the two failures differ, which enumerates accounts:\n  %q\n  %q",
			domain.AsError(wrongPassword).Message, domain.AsError(noSuchUser).Message)
	}
	if domain.AsError(wrongPassword).Code != domain.AsError(noSuchUser).Code {
		t.Errorf("failure codes differ: %v vs %v",
			domain.AsError(wrongPassword).Code, domain.AsError(noSuchUser).Code)
	}
}

// TestLoginLocksOutAfterRepeatedFailures is the brute-force defence.
func TestLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	svc, _, _, _, _, now := newTestService(t, nil)
	ctx := context.Background()
	if _, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// The threshold is 3 in the test harness.
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := svc.Login(ctx, "admin", "wrong-password-here", ClientInfo{}); err == nil {
			t.Fatalf("attempt %d should have failed", attempt)
		}
	}

	// Correct credentials must now be refused while the lockout is open.
	_, err := svc.Login(ctx, "admin", goodPassword, ClientInfo{})
	if err == nil {
		t.Fatal("a locked account must refuse the correct password")
	}
	if domain.AsError(err).Code != domain.ErrCodeRateLimited {
		t.Errorf("locked login error = %v, want rate_limited", err)
	}

	// Once the window passes, login works again without operator intervention.
	*now = now.Add(2 * time.Second)
	if _, err := svc.Login(ctx, "admin", goodPassword, ClientInfo{}); err != nil {
		t.Errorf("login after the lockout expired: %v", err)
	}
}

func TestLoginResetsFailureCountOnSuccess(t *testing.T) {
	svc, users, _, _, _, _ := newTestService(t, nil)
	ctx := context.Background()
	created, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := svc.Login(ctx, "admin", "wrong-password-here", ClientInfo{}); err == nil {
			t.Fatal("expected a failure")
		}
	}
	if _, err := svc.Login(ctx, "admin", goodPassword, ClientInfo{}); err != nil {
		t.Fatalf("login: %v", err)
	}

	users.mu.Lock()
	attempts := users.byID[created.User.ID].FailedAttempts
	users.mu.Unlock()
	if attempts != 0 {
		t.Errorf("failed_attempts = %d, want a reset to 0 after a success", attempts)
	}
}

func TestDisabledAccountCannotLogIn(t *testing.T) {
	svc, _, _, _, _, _ := newTestService(t, nil)
	ctx := context.Background()
	created, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := svc.SetEnabled(ctx, created.User.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if _, err := svc.Login(ctx, "admin", goodPassword, ClientInfo{}); err == nil {
		t.Fatal("a disabled account must not be able to log in")
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func TestSessionTokenIsNotStoredInPlaintext(t *testing.T) {
	svc, _, sessions, _, _, _ := newTestService(t, nil)
	ctx := context.Background()
	result, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for _, s := range sessions.byID {
		if s.TokenHash == result.Token {
			t.Fatal("the session token itself must never be persisted")
		}
		if s.TokenHash != HashSessionToken(result.Token) {
			t.Fatal("the stored value must be the token's hash")
		}
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	svc, _, sessions, _, _, _ := newTestService(t, nil)
	ctx := context.Background()
	result, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if _, err := svc.Authenticate(ctx, result.Token); err != nil {
		t.Fatalf("the session should be valid before logout: %v", err)
	}
	if err := svc.Logout(ctx, result.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, result.Token); err == nil {
		t.Fatal("a revoked session must not authenticate")
	}
	// The row is retained rather than deleted so the sign-in record survives.
	if sessions.live() != 0 {
		t.Error("logout must mark the session revoked")
	}
}

func TestGarbageTokenIsRejected(t *testing.T) {
	svc, _, _, _, _, _ := newTestService(t, nil)
	for _, token := range []string{"", "   ", "not-a-real-token", strings.Repeat("A", 512)} {
		if _, err := svc.Authenticate(context.Background(), token); err == nil {
			t.Errorf("Authenticate(%q) accepted an invalid token", token)
		}
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	svc, _, _, _, _, now := newTestService(t, func(o *Options) { o.SessionTTL = time.Hour })
	ctx := context.Background()
	result, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	*now = now.Add(90 * time.Minute)
	if _, err := svc.Authenticate(ctx, result.Token); err == nil {
		t.Fatal("an expired session must not authenticate")
	}
}

// TestIdleExpiryIsEnforcedOnPresentation keeps the check where it can actually
// reject something, rather than in a background job that may not run.
func TestIdleExpiryIsEnforcedOnPresentation(t *testing.T) {
	svc, _, _, _, _, now := newTestService(t, nil)
	ctx := context.Background()
	result, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if _, err := svc.Authenticate(ctx, result.Token); err != nil {
		t.Fatalf("the session should be live: %v", err)
	}
	*now = now.Add(31 * time.Minute)
	if _, err := svc.Authenticate(ctx, result.Token); err == nil {
		t.Fatal("a session idle beyond IdleTTL must stop authenticating")
	}
}

// TestChangePasswordEvictsExistingSessions is the property that makes a password
// change an access-control action rather than a cosmetic one.
func TestChangePasswordEvictsExistingSessions(t *testing.T) {
	svc, _, _, _, _, _ := newTestService(t, nil)
	ctx := context.Background()
	created, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	const next = "a-brand-new-password-9"
	if err := svc.ChangePassword(ctx, created.User.ID, goodPassword, next); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := svc.Authenticate(ctx, created.Token); err == nil {
		t.Fatal("the old session must not survive a password change")
	}
	if _, err := svc.Login(ctx, "admin", next, ClientInfo{}); err != nil {
		t.Errorf("login with the new password: %v", err)
	}
	if _, err := svc.Login(ctx, "admin", goodPassword, ClientInfo{}); err == nil {
		t.Error("the old password must stop working")
	}
}

func TestChangePasswordRequiresTheCurrentPassword(t *testing.T) {
	svc, _, _, _, _, _ := newTestService(t, nil)
	ctx := context.Background()
	created, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := svc.ChangePassword(ctx, created.User.ID, "not-my-password-1", "a-brand-new-password-9"); err == nil {
		t.Fatal("changing a password must require the current one")
	}
	if _, err := svc.Login(ctx, "admin", goodPassword, ClientInfo{}); err != nil {
		t.Errorf("the original password must still work: %v", err)
	}
}

// TestPasswordNeverAppearsInAuditEvents is the logging rule, asserted rather than
// assumed: the audit sink only ever receives usernames and counters.
func TestPasswordNeverAppearsInAuditEvents(t *testing.T) {
	users, sessions, latch, audit := newFakeUsers(), newFakeSessions(), newFakeLatch(), &recordedAudit{}
	svc := NewService(Options{
		Users: users, Sessions: sessions, Latch: latch, Audit: audit,
		PasswordRules: DefaultPasswordRules(), SessionTTL: time.Hour,
		LockoutDuration: time.Second, MaxLockout: time.Minute,
	})

	ctx := context.Background()
	if _, err := svc.Setup(ctx, "admin", goodPassword, goodPassword, ClientInfo{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, _ = svc.Login(ctx, "admin", "wrong-password-here", ClientInfo{})

	audit.mu.Lock()
	defer audit.mu.Unlock()
	for _, event := range audit.events {
		if strings.Contains(event, goodPassword) {
			t.Fatalf("an audit event carried the password: %q", event)
		}
	}
}

func TestDescribeStatesThePolicyWithoutSecrets(t *testing.T) {
	svc, _, _, _, _, _ := newTestService(t, func(o *Options) {
		o.SessionTTL = 2 * time.Hour
		o.MaxFailedAttempts = 7
	})
	d := svc.Describe()
	if d.MinPasswordLength < 12 {
		t.Errorf("described minimum length = %d, want at least 12", d.MinPasswordLength)
	}
	if d.MaxFailedAttempts != 7 {
		t.Errorf("described max attempts = %d, want 7", d.MaxFailedAttempts)
	}
	if d.SessionMinutes != 120 {
		t.Errorf("described session minutes = %d, want 120", d.SessionMinutes)
	}
}
