package workrun

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticLifetimeConnectionAuthenticatesAndClosesAfterService(t *testing.T) {
	directory := t.TempDir()
	service, err := startDiagnosticServiceAt(directory, "127.0.0.1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	data, err := os.ReadFile(filepath.Join(directory, "service.json"))
	if err != nil {
		t.Fatal(err)
	}
	var descriptor struct {
		Capability string
		Port       int
	}
	if err := json.Unmarshal(data, &descriptor); err != nil {
		t.Fatal(err)
	}
	address := service.listener.Addr().String()
	connect := func(capability string) net.Conn {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		connection.SetDeadline(time.Now().Add(time.Second))
		if _, err := io.WriteString(connection, capability); err != nil {
			t.Fatal(err)
		}
		return connection
	}
	rejected := connect(strings.Repeat("0", 64))
	defer rejected.Close()
	var acknowledgement [1]byte
	if _, err := rejected.Read(acknowledgement[:]); err == nil {
		t.Fatal("unauthenticated lifetime connection acknowledged")
	}
	accepted := connect(descriptor.Capability)
	defer accepted.Close()
	if _, err := io.ReadFull(accepted, acknowledgement[:]); err != nil || acknowledgement[0] != 1 {
		t.Fatal("authenticated lifetime connection rejected", err)
	}
	service.Close()
	if _, err := accepted.Read(acknowledgement[:]); err != io.EOF {
		t.Fatal("service cleanup did not close lifetime connection", err)
	}
}
