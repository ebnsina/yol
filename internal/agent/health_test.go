package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/ebnsina/yol/internal/proto"
)

// answering starts something that replies as told and returns where to reach it.
func answering(t *testing.T, status int) (string, int) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)

	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return host, number
}

func TestAVersionThatAnswersPassesTheCheck(t *testing.T) {
	host, port := answering(t, http.StatusOK)

	gate := &proto.HealthGate{HTTPPath: "/healthz", Port: port}
	if err := probe(context.Background(), host, gate); err != nil {
		t.Errorf("a version answering normally was judged unhealthy: %v", err)
	}
}

// An app that starts, listens, and then returns an error on every request is broken, and putting
// traffic onto it would be worse than leaving the old version serving.
func TestAVersionAnsweringWithAnErrorFailsTheCheck(t *testing.T) {
	host, port := answering(t, http.StatusInternalServerError)

	gate := &proto.HealthGate{HTTPPath: "/healthz", Port: port}
	if err := probe(context.Background(), host, gate); err == nil {
		t.Error("a version answering with an error was judged healthy")
	}
}

func TestNothingListeningFailsTheCheck(t *testing.T) {
	// Bound and closed, so the port is one nothing is listening on.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	_ = listener.Close()

	if err := probe(context.Background(), host, &proto.HealthGate{TCPPort: number}); err == nil {
		t.Error("a port with nothing listening was judged healthy")
	}
}

// A service that speaks no HTTP at all, such as a database, is checked by connecting to it.
func TestSomethingAcceptingConnectionsPassesTheCheck(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)

	if err := probe(context.Background(), host, &proto.HealthGate{TCPPort: number}); err != nil {
		t.Errorf("something accepting connections was judged unhealthy: %v", err)
	}
}

// A check naming nothing to check cannot pass, because passing would mean traffic moves onto a
// version nothing has looked at.
func TestACheckThatNamesNothingCannotPass(t *testing.T) {
	if err := probe(context.Background(), "127.0.0.1", &proto.HealthGate{}); err == nil {
		t.Error("a check with neither a path nor a port passed")
	}
}

// Giving up has to happen, or a deploy of something that never starts would hang forever.
func TestTheCheckGivesUp(t *testing.T) {
	agent := buildingAgent(t)

	gate := &proto.HealthGate{TCPPort: 1, TimeoutSec: 1, IntervalSec: 1}
	if err := agent.awaitHealthy(context.Background(), "not-a-container", gate); err == nil {
		t.Error("the check on a container that does not exist passed")
	}
}
