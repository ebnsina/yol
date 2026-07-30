package server

import (
	"testing"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/google/uuid"
)

func livePlacement(kind sqlc.ServiceKind, port int32) sqlc.ListLivePlacementsForServerRow {
	image := "yol/app:abc123"
	serviceID := uuid.New()
	deploymentID := uuid.New()

	return sqlc.ListLivePlacementsForServerRow{
		ServiceID:     serviceID,
		DeploymentID:  deploymentID,
		ContainerName: ContainerNameFor(serviceID, deploymentID),
		ImageRef:      &image,
		Kind:          kind,
		HealthPort:    &port,
	}
}

// A newly deployed app has no domain, so it has to be reachable by the address of the server it
// was placed on.
func TestASingleAppIsReachedByTheServersAddress(t *testing.T) {
	app := livePlacement(sqlc.ServiceKindApp, 3000)

	route := fallbackRoute([]sqlc.ListLivePlacementsForServerRow{app})
	if route == nil {
		t.Fatal("nothing answers requests arriving by address, so the app cannot be opened")
	}
	if route.Host != "" {
		t.Errorf("host = %q, want empty so that any request matches", route.Host)
	}
	if route.Container != app.ContainerName || route.Port != 3000 {
		t.Errorf("route points at %s:%d, want the app's own container", route.Container, route.Port)
	}
}

// With more than one app on a machine an address does not say which was meant, and guessing would
// serve one customer's project at another's request.
func TestNothingIsServedByAddressWhenSeveralAppsShareAServer(t *testing.T) {
	placements := []sqlc.ListLivePlacementsForServerRow{
		livePlacement(sqlc.ServiceKindApp, 3000),
		livePlacement(sqlc.ServiceKindApp, 8080),
	}

	if route := fallbackRoute(placements); route != nil {
		t.Errorf("requests by address reach %s, chosen from two apps", route.Container)
	}
}

// Databases are not web servers, so they must never be what an address reaches.
func TestServicesThatAreNotAppsAreNeverServedByAddress(t *testing.T) {
	placements := []sqlc.ListLivePlacementsForServerRow{
		livePlacement(sqlc.ServiceKindPostgres, 5432),
		livePlacement(sqlc.ServiceKindRedis, 6379),
	}

	if route := fallbackRoute(placements); route != nil {
		t.Errorf("requests by address reach %s, which serves no web traffic", route.Container)
	}
}

func TestAServerWithNothingDeployedServesNothingByAddress(t *testing.T) {
	if route := fallbackRoute(nil); route != nil {
		t.Errorf("requests by address reach %s on a server running nothing", route.Container)
	}
}

// A service whose deployment has not produced an image yet is not running, so an address must not
// be pointed at it.
func TestAnAppWithNothingBuiltIsNotServedByAddress(t *testing.T) {
	placement := livePlacement(sqlc.ServiceKindApp, 3000)
	placement.ImageRef = nil

	if route := fallbackRoute([]sqlc.ListLivePlacementsForServerRow{placement}); route != nil {
		t.Errorf("requests by address reach %s, which has no image to run", route.Container)
	}
}

// An app that never said which port it listens on is assumed to be on the usual one.
func TestAnAppWithNoPortGivenUsesTheUsualOne(t *testing.T) {
	placement := livePlacement(sqlc.ServiceKindApp, 0)
	placement.HealthPort = nil

	route := fallbackRoute([]sqlc.ListLivePlacementsForServerRow{placement})
	if route == nil || route.Port != 80 {
		t.Fatalf("route = %+v, want port 80", route)
	}
}

// Two versions of one app have to be able to run at once, or there is no way to start the new one
// before taking the old one away.
func TestTwoDeploymentsOfOneServiceGetDifferentContainers(t *testing.T) {
	serviceID := uuid.New()

	first := ContainerNameFor(serviceID, uuid.New())
	second := ContainerNameFor(serviceID, uuid.New())

	if first == second {
		t.Errorf("both deployments are named %s, so one would replace the other", first)
	}
}

// The name has to be the same every time it is worked out, since the agent is told to run it and
// the control plane is told what is running.
func TestAContainerIsNamedTheSameWayEveryTime(t *testing.T) {
	serviceID, deploymentID := uuid.New(), uuid.New()

	if ContainerNameFor(serviceID, deploymentID) != ContainerNameFor(serviceID, deploymentID) {
		t.Error("the same deployment produced two different container names")
	}
}

// A service that named a health path is checked by asking for it, because a container can accept a
// connection well before it can answer a request.
func TestAServiceWithAHealthPathIsCheckedByAskingForIt(t *testing.T) {
	placement := livePlacement(sqlc.ServiceKindApp, 3000)
	path := "/healthz"
	placement.HealthPath = &path

	container := containerFor(placement, uuid.New())
	if container.HealthCheck == nil {
		t.Fatal("no health check, so traffic would move to a version that never answered")
	}
	if container.HealthCheck.HTTPPath != "/healthz" || container.HealthCheck.Port != 3000 {
		t.Errorf("check = %+v, want the path and port the service named", container.HealthCheck)
	}
}

// Without a path there is still a check, since starting a container proves nothing on its own.
func TestAServiceWithNoHealthPathIsStillChecked(t *testing.T) {
	container := containerFor(livePlacement(sqlc.ServiceKindApp, 3000), uuid.New())

	if container.HealthCheck == nil {
		t.Fatal("no health check at all, so a broken version would be put straight in front of people")
	}
	if container.HealthCheck.HTTPPath != "" || container.HealthCheck.Port != 3000 {
		t.Errorf("check = %+v, want the port alone to be checked", container.HealthCheck)
	}
}

// An app is reached through the router over a private network, so it must not be published to the
// machine where anyone could reach it directly.
func TestAppsArePublishedNowhere(t *testing.T) {
	container := containerFor(livePlacement(sqlc.ServiceKindApp, 3000), uuid.New())

	if len(container.Ports) != 0 {
		t.Errorf("ports = %+v, want an app reachable only through the router", container.Ports)
	}
	if container.Network != "yol" {
		t.Errorf("network = %q, want the private one the router shares", container.Network)
	}
	if container.MemoryLimitBytes == 0 && container.RestartPolicy == "" {
		t.Error("the container carries neither a memory limit nor a restart policy")
	}
}
