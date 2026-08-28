package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Per-company biometric sealing keys (026).
//
// ---------------------------------------------------------------------------
// THIS IS THE ONLY FILE IN THE SERVER THAT HANDLES A SEALING KEY
// ---------------------------------------------------------------------------
//
// And it handles one in exactly one situation: a terminal is collecting its
// device credential and has to be given the key it will seal and unseal with.
// That is the whole of it.
//
// NOTHING ON THE MATERIAL PATH TOUCHES A KEY. Upload, fetch and fan-out
// (database/credential_material.go) validate shapes, check authorisation, and
// move opaque bytes. They never seal, never unseal, and never call anything
// here. So the blast radius of a bug in the material endpoints is bounded to
// routing ciphertext to the wrong terminal -- which the authorisation rules
// govern -- and can never be the disclosure of a key, because those endpoints
// have no key to disclose.
//
// ---------------------------------------------------------------------------
// WHY THIS SECRET IS STORED RECOVERABLY WHEN NO OTHER ONE IS
// ---------------------------------------------------------------------------
//
// Site keys are a SHA-256 (011). Device credentials are a SHA-256 (005).
// Operator invitations are a SHA-256 (017). handlers/announcements.go refuses
// to mint a device key at approval time specifically so that no key sits in a
// row waiting for hardware to arrive.
//
// A hash works for all of those because they are secrets somebody PRESENTS, and
// a hash can verify a presentation. This key is never presented. It is HANDED
// OUT, and a hash cannot be handed out. So it is wrapped instead, under a master
// key that lives in the deployment environment and never in this database.
//
// The property that buys, exactly: the database alone yields nothing. The
// database plus the running server's environment yields every template. That
// second sentence is not a weakness being hidden -- it is the accepted position,
// reviewed and approved, and docs/sealing-key-lifecycle.md is where the full
// threat model lives.

// EnvSealingMasterKey names the environment variable holding the master key
// that wraps every company sealing key: 32 bytes, base64.
const EnvSealingMasterKey = "SEALING_MASTER_KEY"

// Sealing key errors.
var (
	// ErrSealingMasterKeyMissing means the deployment cannot wrap or unwrap.
	// Not the same as "this company has no key": one is a misconfigured server,
	// the other is a company no terminal has ever claimed for.
	ErrSealingMasterKeyMissing = errors.New(
		EnvSealingMasterKey + " is not set: sealing keys cannot be read or created")

	ErrSealingMasterKeyInvalid = errors.New(
		EnvSealingMasterKey + " must be 32 bytes of base64")

	// ErrSealingKeyCorrupt means a wrapped key would not unwrap. Either the
	// master key changed under it, or the row was tampered with. Both are
	// failures to refuse loudly rather than work around.
	ErrSealingKeyCorrupt = errors.New("stored sealing key could not be unwrapped")
)

// masterKey is resolved once. Cached because it is read on every terminal
// collection, and re-decoding base64 on a hot-ish path to re-derive the same 32
// bytes is work with no purpose.
//
// Cached in memory ONLY -- never written anywhere, never logged, and not exposed
// by any accessor that returns it outside this file.
var (
	masterKeyOnce  sync.Once
	masterKeyBytes []byte
	masterKeyLabel string
	masterKeyErr   error
)

// loadMasterKey reads and validates SEALING_MASTER_KEY.
//
// The LABEL is a SHA-256 prefix of the key, not the key. It goes in
// company_sealing_keys.master_key_id so that a row can say which master key
// wrapped it without the database holding anything that helps unwrap it. That is
// what makes rotating the master key possible later: rows wrapped under the old
// one are identifiable.
func loadMasterKey() ([]byte, string, error) {
	masterKeyOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv(EnvSealingMasterKey))
		if raw == "" {
			masterKeyErr = ErrSealingMasterKeyMissing
			return
		}
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) != 32 {
			masterKeyErr = ErrSealingMasterKeyInvalid
			return
		}
		sum := sha256.Sum256(key)
		masterKeyBytes = key
		masterKeyLabel = "mk_" + hex.EncodeToString(sum[:6])
	})
	return masterKeyBytes, masterKeyLabel, masterKeyErr
}

// ResetSealingMasterKeyCache clears the resolved master key.
//
// FOR TESTS. The key is read once per process, which is right for a server and
// wrong for a test binary that has to exercise both the configured and the
// unconfigured case in one run.
func ResetSealingMasterKeyCache() {
	masterKeyOnce = sync.Once{}
	masterKeyBytes = nil
	masterKeyLabel = ""
	masterKeyErr = nil
}

// SealingMasterKeyConfigured reports whether this deployment can wrap or unwrap.
func SealingMasterKeyConfigured() bool {
	_, _, err := loadMasterKey()
	return err == nil
}

// wrapAAD binds a wrapped key to the company and label it was created for.
//
// Without it, a wrapped_key value lifted from one company's row into another's
// would unwrap perfectly and both companies would then be sealing under the same
// key -- a cross-tenant break achieved with nothing but UPDATE. With it, the
// transplant fails to unwrap.
func wrapAAD(companyID int64, keyID string) []byte {
	return []byte(fmt.Sprintf("%d|%s", companyID, keyID))
}

// sealBytes is AES-256-GCM with the nonce prepended.
//
// The same construction the terminals use for template material, deliberately:
// one sealing shape in this system rather than two, so a reader who has
// understood either has understood both.
func sealBytes(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Seal appends to its first argument, so passing the nonce puts the nonce in
	// front of the ciphertext in one allocation.
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// openBytes reverses sealBytes.
func openBytes(key, sealed, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, ErrSealingKeyCorrupt
	}
	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, aad)
	if err != nil {
		return nil, ErrSealingKeyCorrupt
	}
	return plain, nil
}

// newSealingKeyID mints a key label: `ck_` + 12 hex, matching 026's CHECK.
func newSealingKeyID() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "ck_" + hex.EncodeToString(raw), nil
}

// ActiveSealingKeyID returns the label of a company's current key, or "" when it
// has none.
//
// DOES NOT UNWRAP, and that is the point: this is what the material upload path
// uses to check that a terminal sealed under the key the platform expects, and
// that check needs the NAME of the key, never the key. A material endpoint
// therefore never causes a key to be decrypted.
func ActiveSealingKeyID(companyID int64) (string, error) {
	var keyID string
	err := DB.QueryRow(`
		SELECT key_id FROM company_sealing_keys
		 WHERE company_id = $1 AND status = 'ACTIVE'`, companyID).Scan(&keyID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return keyID, nil
}

// AnySealingKeyExists reports whether this installation holds any sealing key.
//
// Used by the startup check: a deployment with sealing keys and no master key
// has permanently lost the ability to give a new terminal the key its fleet is
// already using, and the damage is SILENT -- doors keep working, replication
// just quietly stops for every terminal claimed from then on. Refusing to boot
// converts that into a deploy-time failure somebody actually sees.
func AnySealingKeyExists() (bool, error) {
	// THE TABLE MAY NOT EXIST YET, and that must not be an error.
	//
	// Migrations are not run by the server -- they are applied separately -- so
	// a binary carrying 026 can legitimately start against a database that is
	// still on 025. Without this guard the caller's startup check would ask a
	// question Postgres cannot parse, take that as a failure, and REFUSE TO
	// BOOT: a deploy ordering detail would become an outage of the whole API,
	// for a feature the installation has not even turned on.
	//
	// to_regclass has to be its own round trip rather than a CASE around the
	// EXISTS, because planning the statement is what fails on a missing
	// relation -- a guard inside the same query is evaluated too late to help.
	var present bool
	if err := DB.QueryRow(
		`SELECT to_regclass('public.company_sealing_keys') IS NOT NULL`).Scan(&present); err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}

	var exists bool
	err := DB.QueryRow(`SELECT EXISTS (SELECT 1 FROM company_sealing_keys)`).Scan(&exists)
	return exists, err
}

// EnsureCompanySealingKeyTx returns the company's active key, creating one if it
// has none, and returns it base64 for delivery to a terminal.
//
// LAZY, at first collection rather than at company creation. A company with no
// terminals cannot seal anything, and a key generated before anything could use
// it is a recoverable secret sitting in a database for no reason.
//
// IN THE CALLER'S TRANSACTION, so that a terminal which is recorded as having
// collected its credential and the key that terminal was given are the same
// commit. Two transactions could leave a terminal marked COLLECTED against a key
// row that rolled back.
//
// RETURNS PLAINTEXT. This is the one function in the server that does, and its
// only legitimate caller is the credential collection path. The value must go
// straight into the response body and into nothing else -- not a log line, not
// an audit payload, not a struct that outlives the request.
func EnsureCompanySealingKeyTx(tx *sql.Tx, companyID int64) (keyID string, keyB64 string, err error) {
	master, masterLabel, err := loadMasterKey()
	if err != nil {
		return "", "", err
	}

	var wrapped []byte
	err = tx.QueryRow(`
		SELECT key_id, wrapped_key FROM company_sealing_keys
		 WHERE company_id = $1 AND status = 'ACTIVE'`, companyID).Scan(&keyID, &wrapped)

	switch {
	case err == nil:
		plain, openErr := openBytes(master, wrapped, wrapAAD(companyID, keyID))
		if openErr != nil {
			// The master key changed, or the row was altered. Refusing is the
			// only safe answer: minting a replacement here would silently split
			// the fleet into terminals holding the old key and terminals holding
			// the new one, and material sealed under either would be unreadable
			// by half the doors.
			return "", "", openErr
		}
		return keyID, base64.StdEncoding.EncodeToString(plain), nil

	case errors.Is(err, sql.ErrNoRows):
		// First terminal for this company. Mint.
	default:
		return "", "", err
	}

	plain := make([]byte, 32)
	if _, err := rand.Read(plain); err != nil {
		return "", "", err
	}
	keyID, err = newSealingKeyID()
	if err != nil {
		return "", "", err
	}
	wrapped, err = sealBytes(master, plain, wrapAAD(companyID, keyID))
	if err != nil {
		return "", "", err
	}

	// ON CONFLICT covers the race where two terminals of the same company
	// collect at the same moment. The partial unique index on (company_id) WHERE
	// status = 'ACTIVE' is what makes that a conflict rather than two keys, and
	// this turns the loser of the race into a re-read rather than an error the
	// terminal would see as a failed collection.
	var storedKeyID string
	var storedWrapped []byte
	err = tx.QueryRow(`
		WITH inserted AS (
		    INSERT INTO company_sealing_keys
		        (company_id, key_id, wrapped_key, master_key_id, algorithm, status)
		    VALUES ($1, $2, $3, $4, 'AES-256-GCM', 'ACTIVE')
		    ON CONFLICT DO NOTHING
		    RETURNING key_id, wrapped_key
		)
		SELECT key_id, wrapped_key FROM inserted
		 UNION ALL
		SELECT key_id, wrapped_key FROM company_sealing_keys
		 WHERE company_id = $1 AND status = 'ACTIVE'
		 LIMIT 1`,
		companyID, keyID, wrapped, masterLabel).Scan(&storedKeyID, &storedWrapped)
	if err != nil {
		return "", "", err
	}

	if storedKeyID != keyID {
		// Lost the race: another transaction's key is the active one. Use it.
		existing, openErr := openBytes(master, storedWrapped, wrapAAD(companyID, storedKeyID))
		if openErr != nil {
			return "", "", openErr
		}
		return storedKeyID, base64.StdEncoding.EncodeToString(existing), nil
	}

	return keyID, base64.StdEncoding.EncodeToString(plain), nil
}
