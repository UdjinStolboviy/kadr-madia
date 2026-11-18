package admin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

const (
	waOrigin = "http://localhost:8081"
	waRPID   = "localhost"
)

// softKey is a minimal software authenticator (ES256, "none" attestation).
type softKey struct {
	priv   *ecdsa.PrivateKey
	credID []byte
}

func newSoftKey(t *testing.T) *softKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	return &softKey{priv: k, credID: id}
}

var b64 = base64.RawURLEncoding

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin})
	return b
}

func (k *softKey) register(t *testing.T, options map[string]any, origin string) map[string]any {
	t.Helper()
	pk := options["publicKey"].(map[string]any)
	cd := clientData("webauthn.create", pk["challenge"].(string), origin)
	raw, err := k.priv.PublicKey.Bytes() // uncompressed point: 0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	x, y := raw[1:33], raw[33:65]
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatal(err)
	}
	rp := sha256.Sum256([]byte(waRPID))
	ad := append([]byte{}, rp[:]...)
	ad = append(ad, 0x45) // UP | UV | AT
	ad = append(ad, 0, 0, 0, 0)
	ad = append(ad, make([]byte, 16)...) // aaguid
	ad = binary.BigEndian.AppendUint16(ad, uint16(len(k.credID)))
	ad = append(ad, k.credID...)
	ad = append(ad, cose...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": b64.EncodeToString(k.credID), "rawId": b64.EncodeToString(k.credID), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "attestationObject": b64.EncodeToString(att)},
	}
}

func (k *softKey) assert(t *testing.T, options map[string]any, userHandle string, counter uint32, origin string) map[string]any {
	t.Helper()
	pk := options["publicKey"].(map[string]any)
	cd := clientData("webauthn.get", pk["challenge"].(string), origin)
	rp := sha256.Sum256([]byte(waRPID))
	ad := append([]byte{}, rp[:]...)
	ad = append(ad, 0x05) // UP | UV
	ad = binary.BigEndian.AppendUint32(ad, counter)
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": b64.EncodeToString(k.credID), "rawId": b64.EncodeToString(k.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString([]byte(userHandle)),
		},
	}
}

func (c *client) registerPasskey(t *testing.T, k *softKey, name string) (resp, string) {
	t.Helper()
	b := c.do("POST", "/admin/api/auth/webauthn/register/begin", nil, true)
	if b.code != 200 {
		t.Fatalf("register begin: %d %s", b.code, b.body)
	}
	m := b.json(t)
	cred := k.register(t, m["options"].(map[string]any), waOrigin)
	r := c.do("POST", "/admin/api/auth/webauthn/register/finish",
		map[string]any{"ceremony_id": m["ceremony_id"], "name": name, "credential": cred}, true)
	return r, m["ceremony_id"].(string)
}

func (e *env) passkeyBegin(t *testing.T) (*client, map[string]any) {
	c := &client{e: e}
	r := c.do("POST", "/admin/api/auth/webauthn/login/begin", nil, false)
	if r.code != 200 {
		t.Fatalf("login begin: %d %s", r.code, r.body)
	}
	return c, r.json(t)
}

func adminHandle(t *testing.T, e *env) string {
	a, err := e.store.GetAdminByEmail(t.Context(), testEmail)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func TestWebAuthnRegisterAndLogin(t *testing.T) {
	e := newEnv(t, nil)
	owner := &client{e: e}
	expectProblem(t, owner.do("POST", "/admin/api/auth/webauthn/register/begin", nil, false), 401, "unauthenticated")
	owner.login(t, nil)

	k := newSoftKey(t)
	r, _ := owner.registerPasskey(t, k, "YubiKey")
	if r.code != 201 {
		t.Fatalf("register finish: %d %s", r.code, r.body)
	}
	list := owner.do("GET", "/admin/api/auth/webauthn/credentials", nil, false)
	data := list.json(t)["data"].([]any)
	if list.code != 200 || len(data) != 1 || data[0].(map[string]any)["name"] != "YubiKey" {
		t.Fatalf("list: %d %s", list.code, list.body)
	}
	// Registering the same authenticator again is excluded/conflicting.
	if r2, _ := owner.registerPasskey(t, k, "dup"); r2.code == 201 {
		t.Fatalf("duplicate registration accepted")
	}

	// Passkey login → same session as password login.
	c, m := e.passkeyBegin(t)
	cred := k.assert(t, m["options"].(map[string]any), adminHandle(t, e), 1, waOrigin)
	lr := c.do("POST", "/admin/api/auth/webauthn/login/finish", map[string]any{"ceremony_id": m["ceremony_id"], "credential": cred}, false)
	if lr.code != 200 {
		t.Fatalf("login finish: %d %s", lr.code, lr.body)
	}
	c.csrf = lr.json(t)["csrf_token"].(string)
	if me := c.do("GET", "/admin/api/auth/me", nil, false); me.code != 200 {
		t.Fatalf("me after passkey login: %d", me.code)
	}
	// Replay of the very same response: ceremony already consumed.
	expectProblem(t, c.do("POST", "/admin/api/auth/webauthn/login/finish", map[string]any{"ceremony_id": m["ceremony_id"], "credential": cred}, false), 401, "invalid_credentials")

	// Sign-count regression is rejected.
	c2, m2 := e.passkeyBegin(t)
	cred = k.assert(t, m2["options"].(map[string]any), adminHandle(t, e), 5, waOrigin)
	if r := c2.do("POST", "/admin/api/auth/webauthn/login/finish", map[string]any{"ceremony_id": m2["ceremony_id"], "credential": cred}, false); r.code != 200 {
		t.Fatalf("counter 5: %d %s", r.code, r.body)
	}
	c3, m3 := e.passkeyBegin(t)
	cred = k.assert(t, m3["options"].(map[string]any), adminHandle(t, e), 3, waOrigin)
	expectProblem(t, c3.do("POST", "/admin/api/auth/webauthn/login/finish", map[string]any{"ceremony_id": m3["ceremony_id"], "credential": cred}, false), 401, "invalid_credentials")

	// Delete, then the credential no longer works.
	id := data[0].(map[string]any)["id"].(string)
	expectProblem(t, owner.do("DELETE", "/admin/api/auth/webauthn/credentials/wak_nope", nil, true), 404, "not_found")
	if d := owner.do("DELETE", "/admin/api/auth/webauthn/credentials/"+id, nil, true); d.code != 204 {
		t.Fatalf("delete: %d %s", d.code, d.body)
	}
	c4, m4 := e.passkeyBegin(t)
	cred = k.assert(t, m4["options"].(map[string]any), adminHandle(t, e), 9, waOrigin)
	expectProblem(t, c4.do("POST", "/admin/api/auth/webauthn/login/finish", map[string]any{"ceremony_id": m4["ceremony_id"], "credential": cred}, false), 401, "invalid_credentials")

	// Audit trail.
	var n int
	if err := e.store.DB().R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('auth.webauthn_register','auth.webauthn_delete')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit entries = %d, err %v", n, err)
	}
}

func TestWebAuthnRejections(t *testing.T) {
	e := newEnv(t, nil)
	owner := &client{e: e}
	owner.login(t, nil)
	k := newSoftKey(t)

	// Wrong origin at registration.
	b := owner.do("POST", "/admin/api/auth/webauthn/register/begin", nil, true).json(t)
	bad := k.register(t, b["options"].(map[string]any), "http://evil.example")
	expectProblem(t, owner.do("POST", "/admin/api/auth/webauthn/register/finish",
		map[string]any{"ceremony_id": b["ceremony_id"], "name": "x", "credential": bad}, true), 400, "invalid_credential")

	// Wrong challenge at registration (response built for another ceremony).
	b1 := owner.do("POST", "/admin/api/auth/webauthn/register/begin", nil, true).json(t)
	b2 := owner.do("POST", "/admin/api/auth/webauthn/register/begin", nil, true).json(t)
	other := k.register(t, b2["options"].(map[string]any), waOrigin)
	expectProblem(t, owner.do("POST", "/admin/api/auth/webauthn/register/finish",
		map[string]any{"ceremony_id": b1["ceremony_id"], "name": "x", "credential": other}, true), 400, "invalid_credential")

	// Name required; unknown ceremony.
	expectProblem(t, owner.do("POST", "/admin/api/auth/webauthn/register/finish",
		map[string]any{"ceremony_id": b2["ceremony_id"], "name": " ", "credential": other}, true), 422, "validation_failed")
	expectProblem(t, owner.do("POST", "/admin/api/auth/webauthn/register/finish",
		map[string]any{"ceremony_id": "nope", "name": "x", "credential": other}, true), 400, "invalid_ceremony")

	// A valid registration, then login with a wrong challenge and a foreign key.
	if r, _ := owner.registerPasskey(t, k, "ok"); r.code != 201 {
		t.Fatalf("register: %d %s", r.code, r.body)
	}
	c, m1 := e.passkeyBegin(t)
	_, m2 := e.passkeyBegin(t)
	wrong := k.assert(t, m2["options"].(map[string]any), adminHandle(t, e), 1, waOrigin)
	expectProblem(t, c.do("POST", "/admin/api/auth/webauthn/login/finish", map[string]any{"ceremony_id": m1["ceremony_id"], "credential": wrong}, false), 401, "invalid_credentials")

	stranger := newSoftKey(t)
	c, m := e.passkeyBegin(t)
	cred := stranger.assert(t, m["options"].(map[string]any), adminHandle(t, e), 1, waOrigin)
	expectProblem(t, c.do("POST", "/admin/api/auth/webauthn/login/finish", map[string]any{"ceremony_id": m["ceremony_id"], "credential": cred}, false), 401, "invalid_credentials")

}

func TestWebAuthnDisabledWithoutConfig(t *testing.T) {
	if New(Config{Store: newEnv(t, nil).store}).wa.wa != nil {
		t.Fatal("webauthn must be disabled without RP config")
	}
}
