package mainbackend

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/shared/apiclient"
	"github.com/trueforge-org/dexafix/pkg/shared/must"
	"github.com/trueforge-org/dexafix/pkg/store"
)

// The backend half of two-step verification: the state of an account, the checks of codes, the
// signed cookies, and the step-up proof for the Admin pages.

// mfaState is carried in a signed cookie between the password check and the second step. It is
// not a session: it proves only that the password was right a moment ago.
type mfaState struct {
	UID    int64  `json:"u"`
	Method string `json:"m"`
	Enrol  bool   `json:"e,omitempty"` // the user has to set up two-step verification first
	// PW is a fingerprint of the password the sign-in used. A cookie from before the password was
	// changed or reset is refused: it proved a password that no longer counts.
	PW string `json:"p,omitempty"`
}

// passwordFingerprint identifies the user's current password without revealing it: the start of a
// hash of the stored password hash. It is empty for an account without a password.
func (s *Backend) passwordFingerprint(ctx context.Context, userID int64) (string, error) {
	id, err := s.store.GetIdentity(ctx, models.ProviderLocal, localSubject(userID))
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(id.Secret))
	return base64.RawURLEncoding.EncodeToString(sum[:12]), nil
}

// ---- policy and state ----

// twoFactorPolicy reads who must use two-step verification. A policy that cannot be read is an
// error, not "off": a database that fails for a moment must not let a sign-in skip the step.
func (s *Backend) twoFactorPolicy(ctx context.Context) (string, error) {
	v, err := s.store.GetSetting(ctx, settingRequire2FA)
	if errors.Is(err, store.ErrNotFound) {
		return require2FAOff, nil
	}
	if err != nil {
		return "", err
	}
	switch v {
	case require2FAAdmins, require2FAEveryone:
		return v, nil
	}
	return require2FAOff, nil
}

// twoFactorRequired reports whether the policy covers the user.
func (s *Backend) twoFactorRequired(ctx context.Context, u models.User) (bool, error) {
	policy, err := s.twoFactorPolicy(ctx)
	if err != nil {
		return false, err
	}
	switch policy {
	case require2FAEveryone:
		return true, nil
	case require2FAAdmins:
		return u.IsAdmin(), nil
	}
	return false, nil
}

// hasTwoFactor reports whether the user finished setting up two-step verification.
func (s *Backend) hasTwoFactor(ctx context.Context, userID int64) (bool, error) {
	t, err := s.store.GetTOTP(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil && t.Confirmed, err
}

// secondFactorRefused reports whether too many wrong codes were tried from the client address or
// for the account. When it reports false it has counted this try against the account, before the
// code is checked, so a burst of parallel guesses cannot all pass; the caller resets the count when
// the code is right and counts the address when it is wrong.
func (s *Backend) secondFactorRefused(r *http.Request, userID int64) bool {
	return s.limiter.Blocked(r.Context(), ipKey(r), maxIPFail) || !s.limiter.Attempt(r.Context(), mfaKey(userID), maxMFAFail)
}

func mfaKey(userID int64) string { return "mfa:" + strconv.FormatInt(userID, 10) }

// verifySecondFactor checks a six-digit code or a recovery code. A code works once: the step is
// recorded atomically, so two requests with the same code cannot both succeed.
func (s *Backend) verifySecondFactor(ctx context.Context, userID int64, input string) (ok, recovery bool, err error) {
	if looksLikeTOTP(input) {
		t, err := s.store.GetTOTP(ctx, userID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !t.Confirmed) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		step, good := checkTOTP(t.Secret, input, time.Now())
		if !good || step <= t.LastStep {
			return false, false, nil
		}
		fresh, err := s.store.UseTOTPStep(ctx, userID, step)
		return fresh, false, err
	}
	if strings.TrimSpace(input) == "" {
		return false, false, nil
	}
	ok, err = s.store.UseRecoveryCode(ctx, userID, recoveryHash(input))
	return ok, true, err
}

// pendingTOTPSecret returns the secret of a setup that is not confirmed yet, creating one when
// there is none, so reloading the page keeps showing the same QR code.
func (s *Backend) pendingTOTPSecret(ctx context.Context, userID int64) (string, error) {
	t, err := s.store.GetTOTP(ctx, userID)
	switch {
	case err == nil && t.Confirmed:
		return "", store.ErrExists
	case err == nil:
		return t.Secret, nil
	case !errors.Is(err, store.ErrNotFound):
		return "", err
	}
	secret := newTOTPSecret()
	return secret, s.store.PutPendingTOTP(ctx, userID, secret)
}

func (s *Backend) totpIssuer() string {
	if base := s.cfg().baseURL; base != nil && base.Hostname() != "" {
		return base.Hostname()
	}
	return defaultTOTPIssuer
}

func (s *Backend) twoFactorSetupFor(u models.User, secret string) *twoFactorSetup {
	account := u.Email
	if account == "" {
		account = u.Username
	}
	svg := must.Value(qrSVG(otpauthURI(s.totpIssuer(), account, secret))) // cannot fail: a name or address and a secret are far below the QR capacity
	return &twoFactorSetup{QR: svg, Secret: groupSecret(secret)}
}

// groupSecret writes the secret in groups of four, which is easier to type. Apps ignore spaces.
func groupSecret(secret string) string {
	var b strings.Builder
	for i, c := range secret {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// issueRecoveryCodes replaces the user's recovery codes with a new set and returns it.
func (s *Backend) issueRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes := newRecoveryCodes(recoveryCodeCount)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = recoveryHash(c)
	}
	return codes, s.store.ReplaceRecoveryCodes(ctx, userID, hashes)
}

// ---- the signed cookie between password and second step ----

func (s *Backend) setMFACookie(w http.ResponseWriter, r *http.Request, st mfaState) {
	sealed, _ := must.Value2(s.sealValue(sealMFA, st, mfaTTL)) // cannot fail: the state is a plain struct
	http.SetCookie(w, &http.Cookie{
		Name: mfaCookie, Value: sealed, Path: "/login/", MaxAge: int(mfaTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isSecure(r),
	})
}

func clearMFACookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: mfaCookie, Path: "/login/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isSecure(r)})
}

func (s *Backend) readMFA(r *http.Request) (st mfaState, nonce string, expires time.Time, ok bool) {
	c, err := r.Cookie(mfaCookie)
	if err != nil {
		return st, "", time.Time{}, false
	}
	nonce, expires, ok = s.openValue(sealMFA, c.Value, &st)
	if !ok || st.UID == 0 {
		return st, nonce, expires, false
	}
	// The password must still be the one that was checked. A lookup that fails refuses the cookie.
	now, err := s.passwordFingerprint(r.Context(), st.UID)
	return st, nonce, expires, err == nil && subtle.ConstantTimeCompare([]byte(now), []byte(st.PW)) == 1
}

// secondStep is called by signInTarget for a password sign-in. It returns the page to continue on
// when a second step (or a required setup) comes first, and "" when the sign-in can finish.
func (s *Backend) secondStep(w http.ResponseWriter, r *http.Request, u models.User, method string) (string, error) {
	ctx := r.Context()
	has, err := s.hasTwoFactor(ctx, u.ID)
	if err != nil {
		return "", err
	}
	required, err := s.twoFactorRequired(ctx, u)
	if err != nil {
		return "", err
	}
	if !has && !required {
		return "", nil
	}
	pw, err := s.passwordFingerprint(ctx, u.ID)
	if err != nil {
		return "", err
	}
	if has {
		s.setMFACookie(w, r, mfaState{UID: u.ID, Method: method, PW: pw})
		return "/login/2fa", nil
	}
	s.setMFACookie(w, r, mfaState{UID: u.ID, Method: method, Enrol: true, PW: pw})
	return "/login/2fa/setup", nil
}

// hasLocalPassword reports whether the user can sign in with a password.
func (s *Backend) hasLocalPassword(ctx context.Context, u models.User) (bool, error) {
	_, err := s.store.GetIdentity(ctx, models.ProviderLocal, localSubject(u.ID))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ---- the Admin pages ask again ----

// An administrator with two-step verification on is asked for a code again before the Admin pages,
// even though they are signed in: the Admin pages can change everyone's account, so a stolen
// session cookie alone must not be enough. The proof is a signed cookie for 15 minutes, set here
// and after the second step of a sign-in (so it is not asked twice in a row). It covers /admin only.
// The JSON API for administrators takes access tokens, which have no second step.
const (
	adminCookie    = "dexafix_admin"
	sealAdminStep  = "admin-step-up"
	adminStepUpTTL = 15 * time.Minute
)

type adminStepUp struct {
	UID int64 `json:"u"`
	// S ties the proof to the session it was given in: a copy of the cookie is of no use to anyone
	// who does not hold that session, and signing out ends it.
	S string `json:"s"`
}

// adminSessionBinding is what the proof carries of a session: a hash of its token with a purpose of
// its own, so that it says nothing about the stored session.
func adminSessionBinding(token string) string {
	sum := sha256.Sum256([]byte("admin-step-up:" + token))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

type adminProofKey struct{}

// withAdminProof marks a sign-in as one that also proves the Admin pages (a second step was just
// done): the session it starts then comes with the proof.
func withAdminProof(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), adminProofKey{}, true))
}

// markAdminVerified gives the proof for the session identified by token.
func (s *Backend) markAdminVerified(w http.ResponseWriter, r *http.Request, userID int64, token string) {
	sealed, _ := must.Value2(s.sealValue(sealAdminStep, adminStepUp{UID: userID, S: adminSessionBinding(token)}, adminStepUpTTL)) // cannot fail: a plain struct
	http.SetCookie(w, &http.Cookie{
		Name: adminCookie, Value: sealed, Path: "/admin", MaxAge: int(adminStepUpTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isSecure(r),
	})
}

func (s *Backend) adminVerified(r *http.Request, userID int64) bool {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return false
	}
	sc, err := r.Cookie(sessionCookie)
	if err != nil || sc.Value == "" {
		return false
	}
	var st adminStepUp
	_, _, ok := s.openValue(sealAdminStep, c.Value, &st)
	return ok && st.UID == userID && subtle.ConstantTimeCompare([]byte(st.S), []byte(adminSessionBinding(sc.Value))) == 1
}

// needsAdminStepUp reports whether the administrator has to enter a code before the Admin pages.
func (s *Backend) needsAdminStepUp(r *http.Request, u models.User) (bool, error) {
	has, err := s.hasTwoFactor(r.Context(), u.ID)
	if err != nil || !has {
		return false, err
	}
	return !s.adminVerified(r, u.ID), nil
}

// ---- the internal endpoints ----

func (s *Backend) internalTwoFactorRoutes(mux *http.ServeMux) {
	p := apiclient.Prefix + "/users/{id}/2fa"
	mux.HandleFunc("POST "+p+"/begin", s.internalUser(s.internalTwoFactorBegin))
	mux.HandleFunc("POST "+p+"/confirm", s.internalUser(s.internalTwoFactorConfirm))
	mux.HandleFunc("POST "+p+"/disable", s.internalUser(s.internalTwoFactorDisable))
	mux.HandleFunc("POST "+p+"/recovery", s.internalUser(s.internalTwoFactorRecovery))
	mux.HandleFunc("GET "+apiclient.Prefix+"/admin/step-up", s.internalAdminBasic(s.internalAdminStepUp))
	mux.HandleFunc("POST "+apiclient.Prefix+"/admin/step-up", s.internalAdminBasic(s.internalAdminVerify))
}

// internalAdminBasic allows only administrators, without the step-up proof: for the endpoints that
// ask for it. It answers 401 with CodeUnauthorized when nobody is signed in and 403 with
// CodeForbidden when the user is not an administrator.
func (s *Backend) internalAdminBasic(h func(http.ResponseWriter, *http.Request, models.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil {
			internalFail(w, http.StatusUnauthorized, apiclient.CodeUnauthorized, "sign in first")
			return
		}
		if !u.IsAdmin() {
			internalFail(w, http.StatusForbidden, apiclient.CodeForbidden, "not found")
			return
		}
		h(w, r, *u)
	}
}

// internalAdmin is the guard of every administrator endpoint of the internal API: the user must be
// signed in and an administrator, and an administrator with two-step verification on must have
// proved it recently (the step-up cookie from the visitor's request). It answers CodeUnauthorized,
// CodeForbidden or, when the proof is missing, 403 with CodeStepUp. The frontend's own checks are
// a convenience; this is the one that counts.
func (s *Backend) internalAdmin(h func(http.ResponseWriter, *http.Request, models.User)) http.HandlerFunc {
	return s.internalAdminBasic(func(w http.ResponseWriter, r *http.Request, u models.User) {
		need, err := s.needsAdminStepUp(r, u)
		if err != nil {
			internalError(w, err)
			return
		}
		if need {
			internalFail(w, http.StatusForbidden, apiclient.CodeStepUp, "enter a verification code first")
			return
		}
		h(w, r, u)
	})
}

func (s *Backend) internalAdminStepUp(w http.ResponseWriter, r *http.Request, u models.User) {
	need, err := s.needsAdminStepUp(r, u)
	if err != nil {
		internalError(w, err)
		return
	}
	internalJSON(w, http.StatusOK, map[string]bool{"needed": need})
}

func (s *Backend) internalAdminVerify(w http.ResponseWriter, r *http.Request, u models.User) {
	var in struct{ Code string }
	if !internalDecode(w, r, &in) {
		return
	}
	if s.secondFactorRefused(r, u.ID) {
		s.auditAs(r, u.ID, u.ID, auditSecondFactorThrottled, "admin")
		secondStepPage(w, apiclient.SecondStep{Mode: "admin", Error: "Too many wrong codes. Please wait a few minutes."}, http.StatusTooManyRequests)
		return
	}
	good, _, err := s.verifySecondFactor(r.Context(), u.ID, in.Code)
	if err != nil {
		internalError(w, err)
		return
	}
	if !good {
		s.limiter.Fail(r.Context(), ipKey(r))
		s.auditAs(r, u.ID, u.ID, auditSecondFactorFailed, "admin")
		secondStepPage(w, apiclient.SecondStep{Mode: "admin", Error: secondFactorMessage}, http.StatusUnauthorized)
		return
	}
	s.limiter.Reset(r.Context(), mfaKey(u.ID))
	if sc, err := r.Cookie(sessionCookie); err == nil { // the session the proof is for: this one
		s.markAdminVerified(w, r, u.ID, sc.Value)
	}
	s.audit(r, u.ID, auditAdminVerified, "")
	secondStepRedirect(w, "/admin")
}

// ---- settings ----

// internalCheckSecondFactor verifies the code for a change on the settings page. It reports whether
// to go on; if not, the answer has been written.
func (s *Backend) internalCheckSecondFactor(w http.ResponseWriter, r *http.Request, u models.User, code string) bool {
	if s.secondFactorRefused(r, u.ID) {
		s.auditAs(r, u.ID, u.ID, auditSecondFactorThrottled, "")
		internalFail(w, http.StatusTooManyRequests, apiclient.CodeThrottled, "Too many wrong codes. Try again later.")
		return false
	}
	good, _, err := s.verifySecondFactor(r.Context(), u.ID, code)
	if err != nil {
		internalError(w, err)
		return false
	}
	if !good {
		s.limiter.Fail(r.Context(), ipKey(r))
		s.auditAs(r, u.ID, u.ID, auditSecondFactorFailed, "settings")
		internalFail(w, http.StatusUnprocessableEntity, apiclient.CodeInvalid, secondFactorMessage)
		return false
	}
	s.limiter.Reset(r.Context(), mfaKey(u.ID))
	return true
}

func invalid2FA(w http.ResponseWriter, msg string) {
	internalFail(w, http.StatusUnprocessableEntity, apiclient.CodeInvalid, msg)
}

func (s *Backend) internalTwoFactorBegin(w http.ResponseWriter, r *http.Request, u models.User) {
	var in struct{ Current string }
	if !internalDecode(w, r, &in) {
		return
	}
	ctx := r.Context()
	if has, err := s.hasLocalPassword(ctx, u); err != nil {
		internalError(w, err)
		return
	} else if !has || !s.passwordEnabled(ctx) {
		invalid2FA(w, "Two-step verification protects signing in with a password, which this account does not use.")
		return
	}
	if !s.internalConfirmPassword(w, r, u, in.Current) {
		return
	}
	secret, err := s.pendingTOTPSecret(ctx, u.ID)
	if errors.Is(err, store.ErrExists) {
		invalid2FA(w, "Two-step verification is already on.")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	internalJSON(w, http.StatusOK, apiclient.TwoFactorChange{Setup: s.twoFactorSetupFor(u, secret)})
}

func (s *Backend) internalTwoFactorConfirm(w http.ResponseWriter, r *http.Request, u models.User) {
	var in struct{ Code string }
	if !internalDecode(w, r, &in) {
		return
	}
	ctx := r.Context()
	t, err := s.store.GetTOTP(ctx, u.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.Confirmed) {
		invalid2FA(w, "Start the setup first.")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	// A refusal shows the same secret again, so the person can try the next code.
	refuse := func(msg string, status int) {
		internalJSON(w, http.StatusOK, apiclient.TwoFactorChange{Error: msg, Status: status, Setup: s.twoFactorSetupFor(u, t.Secret)})
	}
	if s.secondFactorRefused(r, u.ID) {
		refuse("Too many wrong codes. Try again later.", http.StatusTooManyRequests)
		return
	}
	step, good := checkTOTP(t.Secret, in.Code, time.Now())
	if !good {
		s.limiter.Fail(r.Context(), ipKey(r))
		s.auditAs(r, u.ID, u.ID, auditSecondFactorFailed, "setup")
		refuse("That code is not right. Check that the app shows the entry for this site and try the next code.", http.StatusUnprocessableEntity)
		return
	}
	if err := s.store.ConfirmTOTP(ctx, u.ID, step); err != nil {
		internalError(w, err)
		return
	}
	codes, err := s.issueRecoveryCodes(ctx, u.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	s.limiter.Reset(r.Context(), mfaKey(u.ID))
	s.audit(r, u.ID, auditTOTPEnabled, "")
	internalJSON(w, http.StatusOK, apiclient.TwoFactorChange{Codes: codes})
}

func (s *Backend) internalTwoFactorDisable(w http.ResponseWriter, r *http.Request, u models.User) {
	var in struct{ Current, Code string }
	if !internalDecode(w, r, &in) {
		return
	}
	ctx := r.Context()
	required, err := s.twoFactorRequired(ctx, u)
	if err != nil {
		internalError(w, err)
		return
	}
	if required {
		internalFail(w, http.StatusForbidden, apiclient.CodeForbidden, "Your administrator requires two-step verification.")
		return
	}
	if !s.internalConfirmPassword(w, r, u, in.Current) || !s.internalCheckSecondFactor(w, r, u, in.Code) {
		return
	}
	if err := s.store.DeleteTOTP(ctx, u.ID); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r, u.ID, auditTOTPDisabled, "")
	internalJSON(w, http.StatusNoContent, nil)
}

func (s *Backend) internalTwoFactorRecovery(w http.ResponseWriter, r *http.Request, u models.User) {
	var in struct{ Current, Code string }
	if !internalDecode(w, r, &in) {
		return
	}
	ctx := r.Context()
	if has, err := s.hasTwoFactor(ctx, u.ID); err != nil {
		internalError(w, err)
		return
	} else if !has {
		invalid2FA(w, "Two-step verification is not on.")
		return
	}
	if !s.internalConfirmPassword(w, r, u, in.Current) || !s.internalCheckSecondFactor(w, r, u, in.Code) {
		return
	}
	codes, err := s.issueRecoveryCodes(ctx, u.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	s.audit(r, u.ID, auditRecoveryRegenerated, "")
	internalJSON(w, http.StatusOK, apiclient.TwoFactorChange{Codes: codes})
}
