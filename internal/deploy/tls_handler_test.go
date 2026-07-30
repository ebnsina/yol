package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// addDomain records a hostname against a service, optionally already verified.
func (f *fixture) addDomain(t *testing.T, serviceID uuid.UUID, hostname string, ours, verified bool) {
	t.Helper()
	ctx := context.Background()

	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		verifiedAt := "NULL"
		if verified {
			verifiedAt = "now()"
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO domains (id, org_id, service_id, hostname, ours, verified_at)
			 VALUES ($1, $2, $3, $4, $5, `+verifiedAt+`)`,
			uuid.New(), f.orgID, serviceID, hostname, ours)
		return err
	})
	if err != nil {
		t.Fatalf("add domain: %v", err)
	}
}

func (f *fixture) askAbout(t *testing.T, hostname string) int {
	t.Helper()
	handler := NewTLSHandler(f.pool)

	mux := http.NewServeMux()
	handler.Routes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/tls/allow?domain="+hostname, nil))
	return recorder.Code
}

// The gate that stops anyone pointing a hostname at a customer's server and making it request
// certificates until the certificate authority refuses.
func TestCertificatesAreRefusedForUnknownHostnames(t *testing.T) {
	f := newFixture(t)

	if code := f.askAbout(t, "someone-elses-domain.example"); code != http.StatusForbidden {
		t.Errorf("status = %d for a hostname nobody added, want 403", code)
	}
}

func TestCertificatesAreAllowedForVerifiedHostnames(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-verified")
	f.addDomain(t, service, "verified.example.com", false, true)

	if code := f.askAbout(t, "verified.example.com"); code != http.StatusOK {
		t.Errorf("status = %d for a verified hostname, want 200", code)
	}
}

// A hostname added but not yet verified must be refused, so a certificate is never obtained for a
// name the user has not shown they control.
func TestCertificatesAreRefusedForUnverifiedHostnames(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-unverified")
	f.addDomain(t, service, "unverified.example.com", false, false)

	if code := f.askAbout(t, "unverified.example.com"); code != http.StatusForbidden {
		t.Errorf("status = %d for an unverified hostname, want 403", code)
	}
}

// A subdomain of one we own needs no verifying, because we control the parent name.
func TestCertificatesAreAllowedForOurOwnSubdomains(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-ours")
	f.addDomain(t, service, "given-out.yol.app", true, false)

	if code := f.askAbout(t, "given-out.yol.app"); code != http.StatusOK {
		t.Errorf("status = %d for a subdomain we own, want 200", code)
	}
}

// An app is reached by the server's address until a domain is added to it, and a certificate is
// never issued for an address, so asking about one must be refused without consulting anything.
func TestCertificatesAreRefusedForAddresses(t *testing.T) {
	f := newFixture(t)

	for _, address := range []string{"203.0.113.10", "203.0.113.10:443", "2001:db8::1", "[2001:db8::1]"} {
		if code := f.askAbout(t, address); code != http.StatusForbidden {
			t.Errorf("status = %d for the address %s, want 403", code, address)
		}
	}
}

func TestCertificatesAreRefusedWithNoHostname(t *testing.T) {
	f := newFixture(t)

	if code := f.askAbout(t, ""); code != http.StatusForbidden {
		t.Errorf("status = %d with no hostname given, want 403", code)
	}
}

// A hostname belonging to another organization must not be usable, which follows from asking
// about the hostname alone rather than about who is asking.
func TestOneOrganizationCannotUseAnothersHostname(t *testing.T) {
	first := newFixture(t)
	service := first.newService(t, "app-first")
	first.addDomain(t, service, "taken.example.com", false, true)

	// Adding the same hostname elsewhere is refused outright, since a hostname resolves to one
	// place and cannot belong to two organizations at once.
	second := newFixture(t)
	other := second.newService(t, "app-second")

	err := second.pool.InOrg(context.Background(), second.orgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO domains (id, org_id, service_id, hostname, ours, verified_at)
			 VALUES ($1, $2, $3, 'taken.example.com', false, now())`,
			uuid.New(), second.orgID, other)
		return err
	})
	if err == nil {
		t.Error("the same hostname was added to a second organization")
	}
}
