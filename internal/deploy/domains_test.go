package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/google/uuid"
)

// What somebody pastes is rarely a bare hostname, and refusing an address they copied from the
// browser would read as a broken form rather than as the wrong thing to paste.
func TestWhatSomebodyPastesIsAccepted(t *testing.T) {
	for _, given := range []string{
		"app.example.com",
		"  app.example.com  ",
		"APP.example.com",
		"https://app.example.com",
		"http://app.example.com/",
		"https://app.example.com/some/path",
		"app.example.com.",
	} {
		cleaned, err := cleanHostname(given)
		if err != nil {
			t.Errorf("%q was refused: %v", given, err)
			continue
		}
		if cleaned != "app.example.com" {
			t.Errorf("%q became %q, want app.example.com", given, cleaned)
		}
	}
}

// An address is not a hostname, and saying so is more use than adding one that could never be
// served or certified.
func TestAnAddressIsRefusedAsAHostname(t *testing.T) {
	for _, given := range []string{"203.0.113.10", "2001:db8::1", "http://203.0.113.10"} {
		if _, err := cleanHostname(given); err == nil {
			t.Errorf("%q was accepted as a hostname", given)
		}
	}
}

func TestSomethingThatIsNotAHostnameIsRefused(t *testing.T) {
	for _, given := range []string{"", "   ", "localhost", "not a hostname", strings.Repeat("a", 260) + ".com"} {
		if _, err := cleanHostname(given); err == nil {
			t.Errorf("%q was accepted as a hostname", given)
		}
	}
}

// The record is spelled out rather than described, and which kind depends on whether the server is
// recorded by address or by name.
func TestTheRecordToCreateIsSpelledOut(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	domain, err := projects.AddDomain(ctx, m, userID, target.EnvironmentID, "shop.example.com")
	if err != nil {
		t.Fatalf("add a domain: %v", err)
	}

	if domain.Verified {
		t.Error("a hostname was served before it was shown to point here")
	}
	if domain.Record == nil {
		t.Fatal("nothing was said about what to create in DNS")
	}
	if domain.Record.Type != "A" || domain.Record.Value == "" {
		t.Errorf("record = %+v, want an A record naming the server's address", domain.Record)
	}
	if domain.Record.Name != "shop.example.com" {
		t.Errorf("record name = %q, want the hostname added", domain.Record.Name)
	}
}

// Until a hostname is verified, the app is reached by its server's address and nothing else, which
// is what the interface has to be able to say.
func TestAnAppIsReachedByAddressUntilAHostnameIsVerified(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	address, err := projects.AddressFor(ctx, m, userID, target.EnvironmentID)
	if err != nil {
		t.Fatalf("read the address: %v", err)
	}
	if !strings.HasPrefix(address.URL, "http://") {
		t.Errorf("url = %q, want the server's address over plain HTTP", address.URL)
	}
	if !address.AddressOnly {
		t.Error("the interface was not told that only an address is available")
	}

	if _, err := projects.AddDomain(ctx, m, userID, target.EnvironmentID, "shop.example.com"); err != nil {
		t.Fatal(err)
	}

	address, err = projects.AddressFor(ctx, m, userID, target.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(address.Domains) != 1 {
		t.Fatalf("domains = %d, want the one added", len(address.Domains))
	}
	if !address.AddressOnly {
		t.Error("an unverified hostname was counted as somewhere the app can be reached")
	}
}

// A hostname pointed somewhere else must not be served: doing so would have a certificate requested
// for a name somebody else controls.
func TestAHostnamePointedElsewhereIsNotServed(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	// example.com resolves, but not to this server, which is the case that matters.
	domain, err := projects.AddDomain(ctx, m, userID, target.EnvironmentID, "example.com")
	if err != nil {
		t.Fatal(err)
	}

	_, err = projects.VerifyDomain(ctx, m, userID, uuid.MustParse(domain.ID))
	if err == nil {
		t.Fatal("a hostname that does not point here was verified")
	}

	var failure *httpx.Error
	if !errors.As(err, &failure) || failure.Code != httpx.CodeInvalidInput {
		t.Errorf("error = %v, want something a user can act on", err)
	}
}

// The same hostname cannot be pointed at two apps: it resolves to one place, and serving it from
// two would make which one answers a matter of chance.
func TestAHostnameCannotBeUsedTwice(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	if _, err := projects.AddDomain(ctx, m, userID, target.EnvironmentID, "taken.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.AddDomain(ctx, m, userID, target.EnvironmentID, "taken.example.com"); err == nil {
		t.Error("the same hostname was added twice")
	}
}

// A hostname needs somewhere to point, so an environment with no server cannot take one.
func TestAHostnameNeedsAServerToPointAt(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	ctx := context.Background()

	created, err := projects.Create(ctx, m, userID, "No Server")
	if err != nil {
		t.Fatal(err)
	}
	envID := uuid.MustParse(created.Environments[0].ID)

	if _, err := projects.AddDomain(ctx, m, userID, envID, "shop.example.com"); err == nil {
		t.Error("a hostname was added to an environment with nowhere to point it")
	}
}

// Someone who may only read must not be able to point a hostname at anything.
func TestAViewerCannotAddAHostname(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	_, userID, target := f.deployable(t, projects)
	viewer := &org.Membership{OrgID: f.orgID, Role: org.RoleViewer}

	_, err := projects.AddDomain(context.Background(), viewer, userID, target.EnvironmentID, "shop.example.com")
	if err == nil {
		t.Error("a viewer added a hostname")
	}
}

// Removing a hostname tells the server, so the router stops answering for it rather than waiting
// for the next pass.
func TestRemovingAHostnameTellsTheServer(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	agents := &recordingAgents{}
	projects.SetAgents(agents)
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	domain, err := projects.AddDomain(ctx, m, userID, target.EnvironmentID, "going.example.com")
	if err != nil {
		t.Fatal(err)
	}

	before := len(agents.reconciled)
	if err := projects.RemoveDomain(ctx, m, userID, uuid.MustParse(domain.ID)); err != nil {
		t.Fatalf("remove a hostname: %v", err)
	}
	if len(agents.reconciled) == before {
		t.Error("the server was never told, so the router would keep answering for it")
	}
}
