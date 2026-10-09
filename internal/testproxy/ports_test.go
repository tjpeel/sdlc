package testproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOfflineTCPPortReconciliationKeepsDynamicPort(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_PROXY_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_PROXY_TESTS=1 for real loopback forwarding")
	}
	p, engine := proxyFixture(t, exampleSession)
	engine.networkIP = "127.0.0.1"
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	port := fmt.Sprintf("%d/tcp", target.Addr().(*net.TCPAddr).Port)
	w := requestProxy(t, p, "POST", "/containers/create", map[string]any{"Image": "public:1", "HostConfig": map[string]any{"PortBindings": map[string][]binding{port: {{HostIP: "127.0.0.1", HostPort: "0"}}}}})
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	id := strings.Repeat("c", 64)
	if err := p.startForwards(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	readPort := func() string {
		w := requestProxy(t, p, "GET", "/containers/"+id+"/json", nil)
		var value struct {
			NetworkSettings struct{ Ports map[string][]binding }
		}
		if json.Unmarshal(w.Body.Bytes(), &value) != nil || len(value.NetworkSettings.Ports[port]) != 1 {
			t.Fatal("published port missing", w.Body.String())
		}
		return value.NetworkSettings.Ports[port][0].HostPort
	}
	first := readPort()
	for range 5 {
		if err := p.startForwards(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if readPort() != first {
			t.Fatal("duplicate start changed dynamic port")
		}
	}
	client, err := net.DialTimeout("tcp", "127.0.0.1:"+first, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(time.Second))
	client.Write([]byte("session data"))
	data := make([]byte, len("session data"))
	if _, err := io.ReadFull(client, data); err != nil || string(data) != "session data" {
		t.Fatal("forward failed", err)
	}
	p.Close()
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+first, time.Second); err == nil {
		c.Close()
		t.Fatal("proxy close retained listener")
	}
}

func TestOfflineUDPForwardKeepsPeerAndMultipleReplies(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_PROXY_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_PROXY_TESTS=1 for real UDP forwarding")
	}
	p, _ := proxyFixture(t, exampleSession)
	target, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	peers := make(chan string, 2)
	go func() {
		buffer := make([]byte, 1024)
		for range 2 {
			n, peer, err := target.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			peers <- peer.String()
			for _, suffix := range []string{"-one", "-two"} {
				target.WriteToUDP(append(append([]byte(nil), buffer[:n]...), []byte(suffix)...), peer)
			}
		}
	}()
	f, err := p.forward("8080/udp", binding{HostIP: "127.0.0.1", HostPort: "0"}, target.LocalAddr().String(), "udp")
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	client, err := net.DialUDP("udp", nil, f.udp.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, 1024)
	for _, message := range []string{"first", "second"} {
		if _, err := client.Write([]byte(message)); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"-one", "-two"} {
			n, err := client.Read(buffer)
			if err != nil || string(buffer[:n]) != message+suffix {
				t.Fatal("UDP reply lost", err, string(buffer[:n]))
			}
		}
	}
	select {
	case first := <-peers:
		select {
		case second := <-peers:
			if first != second {
				t.Fatal("upstream peer changed across packets")
			}
		case <-time.After(time.Second):
			t.Fatal("second datagram absent")
		}
	case <-time.After(time.Second):
		t.Fatal("first datagram absent")
	}
}

func TestOfflineRejectedPortSetLeavesNoListener(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_PROXY_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_PROXY_TESTS=1 for listener rollback")
	}
	p, engine := proxyFixture(t, exampleSession)
	engine.networkIP = "127.0.0.1"
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	hostPort := fmt.Sprint(reservation.Addr().(*net.TCPAddr).Port)
	reservation.Close()
	id := strings.Repeat("c", 64)
	// Both possible mapping iteration orders must leave the requested port free.
	for range 30 {
		p.requested[id] = map[string][]binding{"8080/tcp": {{HostIP: "127.0.0.1", HostPort: hostPort}}, "8081/sctp": {{HostPort: "0"}}}
		if err := p.startForwards(context.Background(), id); err == nil {
			t.Fatal("unsupported port set started")
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal("failed start leaked a listener", err)
		}
		listener.Close()
	}
}
