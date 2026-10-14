package workrun

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// diagnosticService owns only a capability-authenticated lifetime connection.
// It accepts no diagnostic commands or provider credentials over the network.
type diagnosticService struct {
	listener net.Listener
	mu       sync.Mutex
	closed   bool
	clients  map[net.Conn]struct{}
}

func diagnosticServiceAddress() (bind, advertised string, err error) {
	if runtime.GOOS != "linux" {
		return "127.0.0.1", "", nil
	}
	bridge, err := net.InterfaceByName("docker0")
	if err != nil {
		return "", "", fmt.Errorf("live diagnostics require a local Docker bridge for controller liveness")
	}
	addresses, err := bridge.Addrs()
	if err != nil {
		return "", "", err
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil {
			return ip.String(), ip.String(), nil
		}
	}
	return "", "", fmt.Errorf("live diagnostics Docker bridge has no IPv4 address")
}

func startDiagnosticService(directory string) (*diagnosticService, error) {
	bind, advertised, err := diagnosticServiceAddress()
	if err != nil {
		return nil, err
	}
	return startDiagnosticServiceAt(directory, bind, advertised)
}

func startDiagnosticServiceAt(directory, bind, advertised string) (*diagnosticService, error) {
	listener, err := net.Listen("tcp4", net.JoinHostPort(bind, "0"))
	if err != nil {
		return nil, err
	}
	service := &diagnosticService{listener: listener, clients: map[net.Conn]struct{}{}}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		listener.Close()
		return nil, err
	}
	capability := hex.EncodeToString(token)
	descriptor := struct {
		Version    int    `json:"version"`
		Host       string `json:"host,omitempty"`
		Port       int    `json:"port"`
		Capability string `json:"capability"`
	}{1, advertised, listener.Addr().(*net.TCPAddr).Port, capability}
	data, err := json.Marshal(descriptor)
	if err == nil {
		err = os.WriteFile(filepath.Join(directory, "service.json"), data, 0444)
	}
	if err != nil {
		listener.Close()
		return nil, err
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			service.mu.Lock()
			if service.closed {
				service.mu.Unlock()
				connection.Close()
				return
			}
			service.clients[connection] = struct{}{}
			service.mu.Unlock()
			go func() {
				defer func() {
					connection.Close()
					service.mu.Lock()
					delete(service.clients, connection)
					service.mu.Unlock()
				}()
				connection.SetDeadline(time.Now().Add(10 * time.Second))
				supplied := make([]byte, len(capability))
				if _, err := io.ReadFull(connection, supplied); err != nil || subtle.ConstantTimeCompare(supplied, []byte(capability)) != 1 {
					return
				}
				if _, err := connection.Write([]byte{1}); err != nil {
					return
				}
				connection.SetDeadline(time.Time{})
				// Only lifetime is observable: EOF closes this client, never dispatches work.
				var unexpected [1]byte
				connection.Read(unexpected[:])
			}()
		}
	}()
	return service, nil
}

func (service *diagnosticService) Close() {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.closed = true
	service.listener.Close()
	for connection := range service.clients {
		connection.Close()
	}
}
