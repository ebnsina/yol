package server

import (
	"testing"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/google/uuid"
)

func appService(port int32) sqlc.ListServicesForServerRow {
	return sqlc.ListServicesForServerRow{ID: uuid.New(), Kind: sqlc.ServiceKindApp, HealthPort: &port}
}

// A newly deployed app has no domain, so it has to be reachable by the address of the server it
// was placed on.
func TestASingleAppIsReachedByTheServersAddress(t *testing.T) {
	app := appService(3000)

	route := fallbackRoute([]sqlc.ListServicesForServerRow{app})
	if route == nil {
		t.Fatal("nothing answers requests arriving by address, so the app cannot be opened")
	}
	if route.Host != "" {
		t.Errorf("host = %q, want empty so that any request matches", route.Host)
	}
	if route.Container != containerNameFor(app.ID) || route.Port != 3000 {
		t.Errorf("route points at %s:%d, want the app's own container", route.Container, route.Port)
	}
}

// With more than one app on a machine an address does not say which was meant, and guessing would
// serve one customer's project at another's request.
func TestNothingIsServedByAddressWhenSeveralAppsShareAServer(t *testing.T) {
	services := []sqlc.ListServicesForServerRow{appService(3000), appService(8080)}

	if route := fallbackRoute(services); route != nil {
		t.Errorf("requests by address reach %s, chosen from two apps", route.Container)
	}
}

// Databases are not web servers, so they must never be what an address reaches.
func TestServicesThatAreNotAppsAreNeverServedByAddress(t *testing.T) {
	services := []sqlc.ListServicesForServerRow{
		{ID: uuid.New(), Kind: sqlc.ServiceKindPostgres},
		{ID: uuid.New(), Kind: sqlc.ServiceKindRedis},
	}

	if route := fallbackRoute(services); route != nil {
		t.Errorf("requests by address reach %s, which serves no web traffic", route.Container)
	}
}

func TestAServerWithNoAppsServesNothingByAddress(t *testing.T) {
	if route := fallbackRoute(nil); route != nil {
		t.Errorf("requests by address reach %s on a server running nothing", route.Container)
	}
}

// An app that never said which port it listens on is assumed to be on the usual one.
func TestAnAppWithNoPortGivenUsesTheUsualOne(t *testing.T) {
	service := sqlc.ListServicesForServerRow{ID: uuid.New(), Kind: sqlc.ServiceKindApp}

	route := fallbackRoute([]sqlc.ListServicesForServerRow{service})
	if route == nil || route.Port != 80 {
		t.Fatalf("route = %+v, want port 80", route)
	}
}
