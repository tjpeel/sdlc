package testproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type forward struct {
	containerPort, hostIP, port string
	tcp                         net.Listener
	udp                         *net.UDPConn
	cancel                      context.CancelFunc
}

func (f *forward) close() {
	f.cancel()
	if f.tcp != nil {
		f.tcp.Close()
	}
	if f.udp != nil {
		f.udp.Close()
	}
}
func (p *Proxy) stopForwards(id string) {
	p.forwardMu.Lock()
	defer p.forwardMu.Unlock()
	p.stopForwardsLocked(id)
}
func (p *Proxy) stopForwardsLocked(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.ports[id] {
		f.close()
	}
	delete(p.ports, id)
}

func (p *Proxy) startForwards(ctx context.Context, id string) (result error) {
	p.forwardMu.Lock()
	defer p.forwardMu.Unlock()
	if p.ctx.Err() != nil {
		return p.ctx.Err()
	}
	p.mu.Lock()
	requested := p.requested[id]
	started := len(p.ports[id]) > 0
	p.mu.Unlock()
	if len(requested) == 0 || started {
		return nil
	}
	metadata, err := p.raw(ctx, "GET", "/containers/"+id+"/json", nil)
	if err != nil {
		return err
	}
	address := ""
	for _, endpoint := range object(object(metadata["NetworkSettings"])["Networks"]) {
		address = stringValue(object(endpoint)["IPAddress"])
		if net.ParseIP(address) != nil {
			break
		}
	}
	if net.ParseIP(address) == nil {
		return errors.New("published service has no session network address")
	}
	p.stopForwardsLocked(id)
	var forwards []*forward
	defer func() {
		if result != nil {
			for _, f := range forwards {
				f.close()
			}
		}
	}()
	for containerPort, bindings := range requested {
		parts := strings.Split(containerPort, "/")
		port, err := strconv.Atoi(parts[0])
		if err != nil || port < 1 || port > 65535 || len(parts) != 2 || (parts[1] != "tcp" && parts[1] != "udp") {
			return errors.New("unsupported published container port")
		}
		for _, b := range bindings {
			f, err := p.forward(containerPort, b, net.JoinHostPort(address, parts[0]), parts[1])
			if err != nil {
				return err
			}
			forwards = append(forwards, f)
		}
	}
	p.mu.Lock()
	p.ports[id] = forwards
	// Keep dynamically assigned ports stable across automatic restarts.
	stable := map[string][]binding{}
	for _, f := range forwards {
		stable[f.containerPort] = append(stable[f.containerPort], binding{HostIP: f.hostIP, HostPort: f.port})
	}
	p.requested[id] = stable
	p.mu.Unlock()
	return nil
}

func (p *Proxy) forward(containerPort string, b binding, target, protocol string) (*forward, error) {
	host := b.HostIP
	if host == "" {
		host = "0.0.0.0"
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsUnspecified() && !ip.IsLoopback()) {
		return nil, errors.New("published ports must bind localhost or wildcard addresses within the session")
	}
	port := b.HostPort
	if port == "" {
		port = "0"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return nil, errors.New("published port ranges are unavailable in parallel checks")
	}
	ctx, cancel := context.WithCancel(p.ctx)
	f := &forward{containerPort: containerPort, hostIP: host, cancel: cancel}
	if protocol == "tcp" {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
		if err != nil {
			cancel()
			return nil, err
		}
		f.tcp = listener
		f.port = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
		go func() {
			for {
				incoming, err := listener.Accept()
				if err != nil {
					return
				}
				go func() {
					defer incoming.Close()
					outgoing, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", target)
					if err != nil {
						return
					}
					defer outgoing.Close()
					closed := make(chan struct{})
					go func() {
						select {
						case <-ctx.Done():
							incoming.Close()
							outgoing.Close()
						case <-closed:
						}
					}()
					defer close(closed)
					done := make(chan struct{})
					go func() {
						io.Copy(outgoing, incoming)
						if conn, ok := outgoing.(*net.TCPConn); ok {
							conn.CloseWrite()
						}
						close(done)
					}()
					io.Copy(incoming, outgoing)
					incoming.Close()
					outgoing.Close()
					<-done
				}()
			}
		}()
	} else {
		address, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, port))
		if err != nil {
			cancel()
			return nil, err
		}
		listener, err := net.ListenUDP("udp", address)
		if err != nil {
			cancel()
			return nil, err
		}
		f.udp = listener
		f.port = strconv.Itoa(listener.LocalAddr().(*net.UDPAddr).Port)
		go func() {
			var mu sync.Mutex
			flows := map[string]*net.UDPConn{}
			defer func() {
				mu.Lock()
				defer mu.Unlock()
				for _, flow := range flows {
					flow.Close()
				}
			}()
			buffer := make([]byte, 65535)
			for {
				n, sender, err := listener.ReadFromUDP(buffer)
				if err != nil {
					return
				}
				key := sender.String()
				mu.Lock()
				connection := flows[key]
				if connection == nil && len(flows) < 1024 {
					remote, err := net.ResolveUDPAddr("udp", target)
					if err != nil {
						mu.Unlock()
						continue
					}
					connection, err = net.DialUDP("udp", nil, remote)
					if err != nil {
						mu.Unlock()
						continue
					}
					flows[key] = connection
					go func(flow *net.UDPConn, client *net.UDPAddr, key string) {
						defer flow.Close()
						defer func() {
							mu.Lock()
							defer mu.Unlock()
							if flows[key] == flow {
								delete(flows, key)
							}
						}()
						response := make([]byte, 65535)
						for {
							n, err := flow.Read(response)
							if err != nil {
								return
							}
							if _, err := listener.WriteToUDP(response[:n], client); err != nil {
								return
							}
						}
					}(connection, sender, key)
				}
				mu.Unlock()
				if connection != nil {
					connection.SetDeadline(time.Now().Add(30 * time.Second))
					connection.Write(buffer[:n])
				}
			}
		}()
	}
	return f, nil
}

// WatchPorts releases listeners after daemon-driven exits and restores them for
// restart policies. Clients never need access to another session's event stream.
func (p *Proxy) WatchPorts() {
	go func() {
		for p.ctx.Err() == nil {
			filters, _ := json.Marshal(map[string][]string{"label": {SessionLabel + "=" + p.config.Session}, "type": {"container"}})
			request, _ := http.NewRequestWithContext(p.ctx, "GET", "http://docker/events?filters="+url.QueryEscape(string(filters)), nil)
			response, err := p.client.Do(request)
			if err == nil {
				decoder := json.NewDecoder(response.Body)
				for {
					var event struct {
						Action string
						Actor  struct{ ID string }
					}
					if decoder.Decode(&event) != nil {
						break
					}
					switch event.Action {
					case "start":
						p.startForwards(p.ctx, event.Actor.ID)
					case "die", "stop", "destroy":
						p.stopForwards(event.Actor.ID)
					}
				}
				response.Body.Close()
			}
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
}
