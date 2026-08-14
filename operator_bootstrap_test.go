package main

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"

	"access-terminal-cloud-api/bootstrap"
	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// First-operator bootstrap (bootstrap/operator.go, database/bootstrap.go).
//
// The property under test is mostly what this REFUSES to do. It may create an
// OWNER on an empty system and nothing else: no overwriting, no password reset,
// no re-enabling, no role change, and no failure that could stop a running
// deployment from starting.

const bootstrapPassword = "first-owner-secret-passphrase"

// captureLog collects what the bootstrap writes, so a test can assert that the
// password never appears in it.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()

	buffer := &bytes.Buffer{}
	previousOut := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(buffer)
	t.Cleanup(func() {
		log.SetOutput(previousOut)
		log.SetFlags(previousFlags)
	})
	return buffer
}

func liveOperatorCount(t *testing.T) int {
	t.Helper()
	return queryInt(t, `SELECT count(*) FROM users WHERE deleted_at IS NULL`)
}

func TestBootstrapCreatesTheFirstOwner(t *testing.T) {
	cheapBcrypt(t)
	t.Setenv(bootstrap.EnvEmail, "First.Owner@Example.COM")
	t.Setenv(bootstrap.EnvPassword, bootstrapPassword)
	env := newTestEnv(t)
	output := captureLog(t)

	result, err := bootstrap.EnsureFirstOperator()
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if result.Outcome != bootstrap.Created {
		t.Fatalf("outcome = %v, want Created", result.Outcome)
	}

	if n := liveOperatorCount(t); n != 1 {
		t.Fatalf("%d operators exist, want exactly 1", n)
	}

	// Normalised like any other account, and an OWNER because the first account
	// has to be able to create the rest.
	email := queryString(t, `SELECT email FROM users LIMIT 1`)
	if email != "first.owner@example.com" {
		t.Errorf("stored email = %q, want it lowercased", email)
	}
	if role := queryString(t, `SELECT role FROM users LIMIT 1`); role != models.RoleOwner {
		t.Errorf("role = %q, want OWNER", role)
	}
	if !queryBool(t, `SELECT active FROM users LIMIT 1`) {
		t.Error("the bootstrapped owner is not active")
	}

	// The password is hashed with the same policy as any other, and works.
	hash := queryString(t, `SELECT password_hash FROM users LIMIT 1`)
	if strings.Contains(hash, bootstrapPassword) || !strings.HasPrefix(hash, "$2") {
		t.Error("the bootstrap password was not stored as a bcrypt hash")
	}
	if _, err := database.AuthenticatePassword("first.owner@example.com", bootstrapPassword); err != nil {
		t.Errorf("the bootstrapped owner cannot log in: %v", err)
	}

	// It is a real session-capable account: it logs in through the router.
	token, _ := login(t, env.router, "first.owner@example.com", bootstrapPassword)
	code, body, _ := doAuth(t, env.router, authCall{
		method: "GET", path: "/api/v1/auth/me", token: token,
	})
	if code != 200 {
		t.Fatalf("/me as the bootstrapped owner = %d (%v)", code, body)
	}
	if body["role"] != models.RoleOwner {
		t.Errorf("/me role = %v, want OWNER", body["role"])
	}

	// Nothing secret reached the log.
	logged := output.String()
	if strings.Contains(logged, bootstrapPassword) {
		t.Error("the bootstrap password was written to the log")
	}
	if !strings.Contains(logged, "first.owner@example.com") {
		t.Error("the log does not record which account was created")
	}
}

func TestBootstrapNeverOverwritesAnExistingOperator(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	// An existing operator, deliberately NOT an owner and with a different
	// password, sharing the address the bootstrap is configured with.
	existing := mustCreateOperator(t, one, "taken@example.com", models.RoleViewer)
	hashBefore := queryString(t, `SELECT password_hash FROM users WHERE id = $1`, existing.ID)

	t.Setenv(bootstrap.EnvEmail, "taken@example.com")
	t.Setenv(bootstrap.EnvPassword, bootstrapPassword)
	output := captureLog(t)

	result, err := bootstrap.EnsureFirstOperator()
	if err != nil {
		t.Fatalf("bootstrap on a populated system returned an error: %v", err)
	}
	if result.Outcome != bootstrap.SkippedOperatorsExist {
		t.Fatalf("outcome = %v, want SkippedOperatorsExist", result.Outcome)
	}

	if n := liveOperatorCount(t); n != 1 {
		t.Errorf("%d operators exist, want the original 1", n)
	}
	// Not re-passworded, not promoted, not touched.
	if hashAfter := queryString(t, `SELECT password_hash FROM users WHERE id = $1`, existing.ID); hashAfter != hashBefore {
		t.Error("the bootstrap reset an existing operator's password")
	}
	if role := queryString(t, `SELECT role FROM users WHERE id = $1`, existing.ID); role != models.RoleViewer {
		t.Errorf("role = %q, want the original VIEWER", role)
	}
	if _, err := database.AuthenticatePassword("taken@example.com", bootstrapPassword); err == nil {
		t.Error("the bootstrap password now works against the pre-existing account")
	}
	if _, err := database.AuthenticatePassword("taken@example.com", testPassword); err != nil {
		t.Errorf("the pre-existing password stopped working: %v", err)
	}

	// The operator is told the variables are inert, without the password.
	logged := output.String()
	if !strings.Contains(logged, "WARNING") || !strings.Contains(logged, bootstrap.EnvEmail) {
		t.Errorf("no warning that the variables are ignored: %q", logged)
	}
	if strings.Contains(logged, bootstrapPassword) {
		t.Error("the bootstrap password was written to the log")
	}

	// A DISABLED operator still counts as live: the bootstrap must not become a
	// way back in when someone has switched the only account off.
	if err := database.SetUserActive(one, existing.ID, false); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	result, err = bootstrap.EnsureFirstOperator()
	if err != nil || result.Outcome != bootstrap.SkippedOperatorsExist {
		t.Errorf("bootstrap with a disabled operator = %v, %v; want a skip",
			result.Outcome, err)
	}
	if n := liveOperatorCount(t); n != 1 {
		t.Errorf("%d operators exist after the disabled-account attempt, want 1", n)
	}
}

func TestBootstrapConfigurationRules(t *testing.T) {
	cheapBcrypt(t)

	t.Run("neither variable set does nothing quietly", func(t *testing.T) {
		newTestEnv(t)
		t.Setenv(bootstrap.EnvEmail, "")
		t.Setenv(bootstrap.EnvPassword, "")
		output := captureLog(t)

		result, err := bootstrap.EnsureFirstOperator()
		if err != nil {
			t.Fatalf("unconfigured bootstrap errored: %v", err)
		}
		if result.Outcome != bootstrap.NotConfigured {
			t.Errorf("outcome = %v, want NotConfigured", result.Outcome)
		}
		if liveOperatorCount(t) != 0 {
			t.Error("an operator was created with no configuration")
		}
		if output.Len() != 0 {
			t.Errorf("the unconfigured path logged something: %q", output.String())
		}
	})

	// A half-configured bootstrap is a mistake, and which half was meant is not
	// something to guess at.
	halves := []struct{ name, email, password string }{
		{"only the email", "half@example.com", ""},
		{"only the password", "", bootstrapPassword},
	}
	for _, tc := range halves {
		t.Run(tc.name+" fails startup", func(t *testing.T) {
			newTestEnv(t)
			t.Setenv(bootstrap.EnvEmail, tc.email)
			t.Setenv(bootstrap.EnvPassword, tc.password)

			_, err := bootstrap.EnsureFirstOperator()
			if err == nil {
				t.Fatal("a half-configured bootstrap was accepted")
			}
			if !strings.Contains(err.Error(), bootstrap.EnvEmail) ||
				!strings.Contains(err.Error(), bootstrap.EnvPassword) {
				t.Errorf("the error does not name both variables: %v", err)
			}
			if strings.Contains(err.Error(), bootstrapPassword) {
				t.Error("the error message contains the password")
			}
			if liveOperatorCount(t) != 0 {
				t.Error("an operator was created from a half-configured bootstrap")
			}
		})
	}

	// Bad values fail, and only in the case where the bootstrap would run.
	invalid := []struct{ name, email, password, company string }{
		{"malformed email", "not-an-address", bootstrapPassword, ""},
		{"password below the policy", "owner@example.com", "short", ""},
		{"unknown company slug", "owner@example.com", bootstrapPassword, "no-such-company"},
	}
	for _, tc := range invalid {
		t.Run(tc.name+" fails startup", func(t *testing.T) {
			newTestEnv(t)
			t.Setenv(bootstrap.EnvEmail, tc.email)
			t.Setenv(bootstrap.EnvPassword, tc.password)
			t.Setenv(bootstrap.EnvCompany, tc.company)

			_, err := bootstrap.EnsureFirstOperator()
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if strings.Contains(err.Error(), tc.password) && tc.password != "short" {
				t.Error("the error message contains the password")
			}
			if liveOperatorCount(t) != 0 {
				t.Errorf("%s still created an operator", tc.name)
			}
		})
	}

	// The same bad values are harmless once the system has an operator: they are
	// never read for their content, so a stale variable cannot stop the API.
	t.Run("bad values are inert on a populated system", func(t *testing.T) {
		newTestEnv(t)
		one := operatorCompanyID(t, "one")
		mustCreateOperator(t, one, "already@example.com", models.RoleOwner)

		t.Setenv(bootstrap.EnvEmail, "not-an-address")
		t.Setenv(bootstrap.EnvPassword, "short")
		t.Setenv(bootstrap.EnvCompany, "no-such-company")

		result, err := bootstrap.EnsureFirstOperator()
		if err != nil {
			t.Fatalf("a stale, invalid bootstrap stopped a running system: %v", err)
		}
		if result.Outcome != bootstrap.SkippedOperatorsExist {
			t.Errorf("outcome = %v, want SkippedOperatorsExist", result.Outcome)
		}
	})
}

func TestBootstrapCompanySelection(t *testing.T) {
	cheapBcrypt(t)

	t.Run("defaults to the oldest company", func(t *testing.T) {
		newTestEnv(t)
		t.Setenv(bootstrap.EnvEmail, "oldest@example.com")
		t.Setenv(bootstrap.EnvPassword, bootstrapPassword)
		t.Setenv(bootstrap.EnvCompany, "")

		if _, err := bootstrap.EnsureFirstOperator(); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		slug := queryString(t, `SELECT c.slug FROM users u
		                          JOIN companies c ON c.id = u.company_id LIMIT 1`)
		if slug != "one" {
			t.Errorf("owner joined company %q, want the oldest company", slug)
		}
	})

	t.Run("honours an explicit slug", func(t *testing.T) {
		newTestEnv(t)
		t.Setenv(bootstrap.EnvEmail, "chosen@example.com")
		t.Setenv(bootstrap.EnvPassword, bootstrapPassword)
		t.Setenv(bootstrap.EnvCompany, "two")

		if _, err := bootstrap.EnsureFirstOperator(); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		slug := queryString(t, `SELECT c.slug FROM users u
		                          JOIN companies c ON c.id = u.company_id LIMIT 1`)
		if slug != "two" {
			t.Errorf("owner joined company %q, want the configured company", slug)
		}
	})
}

func TestBootstrapIsSafeWhenTwoInstancesStartTogether(t *testing.T) {
	cheapBcrypt(t)
	t.Setenv(bootstrap.EnvEmail, "race@example.com")
	t.Setenv(bootstrap.EnvPassword, bootstrapPassword)
	newTestEnv(t)
	captureLog(t)

	// Two instances starting at once -- a rolling deploy, an overlapping restart
	// -- must not both insert. The advisory lock serialises them, so the second
	// sees the first's committed row and skips.
	const instances = 4
	results := make([]bootstrap.Result, instances)
	errs := make([]error, instances)

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < instances; i++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			results[index], errs[index] = bootstrap.EnsureFirstOperator()
		}(i)
	}
	start.Done()
	done.Wait()

	created := 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("instance %d failed: %v", i, err)
		}
		if results[i].Outcome == bootstrap.Created {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d instances reported creating the owner, want exactly 1", created)
	}
	if n := liveOperatorCount(t); n != 1 {
		t.Errorf("%d operator rows exist, want exactly 1", n)
	}
}

func TestBootstrapRunsAgainOnceEveryOperatorIsRetired(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	existing := mustCreateOperator(t, one, "retiring@example.com", models.RoleOwner)

	t.Setenv(bootstrap.EnvEmail, "recovered@example.com")
	t.Setenv(bootstrap.EnvPassword, bootstrapPassword)
	captureLog(t)

	if result, _ := bootstrap.EnsureFirstOperator(); result.Outcome != bootstrap.SkippedOperatorsExist {
		t.Fatalf("outcome = %v, want a skip while an operator exists", result.Outcome)
	}

	// This is the documented total-lockout recovery: retire the accounts, then
	// redeploy with the variables set. It requires database access, which is
	// already full-trust -- there is deliberately no flag that forces a
	// re-bootstrap over live accounts.
	if err := database.SoftDeleteUser(one, existing.ID); err != nil {
		t.Fatalf("retiring the last operator: %v", err)
	}

	result, err := bootstrap.EnsureFirstOperator()
	if err != nil {
		t.Fatalf("bootstrap after retirement: %v", err)
	}
	if result.Outcome != bootstrap.Created {
		t.Fatalf("outcome = %v, want Created once no live operator remains", result.Outcome)
	}
	if n := liveOperatorCount(t); n != 1 {
		t.Errorf("%d live operators, want the new one only", n)
	}
	if _, err := database.AuthenticatePassword("recovered@example.com", bootstrapPassword); err != nil {
		t.Errorf("the recovered owner cannot log in: %v", err)
	}
}
