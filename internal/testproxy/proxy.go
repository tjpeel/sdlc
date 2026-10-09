// Package testproxy scopes Docker's Engine API to one disposable check session.
// The controller owns the daemon and this proxy; repository code sees only the
// session socket. Images and build cache remain in the shared daemon.
package testproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

const SessionLabel = "io.sdlc.test-session"
const InternalLabel = "io.sdlc.test-internal"

var sessionPattern = regexp.MustCompile(`^sdlc-check-[a-f0-9]{24}$`)
var apiVersion = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)
var objectID = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

type Config struct {
	Session, Socket, ProxyID, Workspace, SocketSource string
	LocalWorkspace                                    string
}

type Proxy struct {
	config     Config
	client     *http.Client
	reverse    *httputil.ReverseProxy
	sequence   atomic.Uint64
	mu         sync.Mutex
	forwardMu  sync.Mutex
	images     map[string]string
	imageNames map[string]string
	ports      map[string][]*forward
	requested  map[string]map[string][]binding
	ctx        context.Context
	cancel     context.CancelFunc
}

func New(config Config) (*Proxy, error) {
	if !sessionPattern.MatchString(config.Session) || config.Socket == "" || !objectID.MatchString(config.ProxyID) || config.Workspace != "/sdlc/workspaces/"+config.Session || config.SocketSource != "/sdlc/sockets/"+config.Session+"/docker.sock" {
		return nil, errors.New("invalid test proxy configuration")
	}
	if config.LocalWorkspace == "" {
		config.LocalWorkspace = "/workspace"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", config.Socket)
	}, DisableCompression: true}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{config: config, client: &http.Client{Transport: transport}, images: map[string]string{}, imageNames: map[string]string{}, ports: map[string][]*forward{}, requested: map[string]map[string][]binding{}, ctx: ctx, cancel: cancel}
	p.reverse = &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "docker"
		pr.Out.Host = "docker"
	}, Transport: transport, ModifyResponse: p.response, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		failure(w, http.StatusBadGateway, "shared test daemon transport failed")
	}}
	return p, nil
}

func (p *Proxy) Close() error {
	p.cancel()
	p.forwardMu.Lock()
	defer p.forwardMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, list := range p.ports {
		for _, f := range list {
			f.close()
		}
	}
	p.ports = map[string][]*forward{}
	return nil
}

func failure(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"message": message})
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	u := *r.URL
	r.URL = &u
	endpoint := apiVersion.ReplaceAllString(r.URL.Path, "")
	if path.Clean(endpoint) != endpoint || strings.Contains(endpoint, "\x00") {
		failure(w, 400, "invalid Docker API path")
		return
	}
	if err := p.request(r, endpoint); err != nil {
		code := http.StatusForbidden
		var upstream *apiError
		if errors.As(err, &upstream) {
			code = upstream.code
		}
		failure(w, code, err.Error())
		return
	}
	p.reverse.ServeHTTP(w, r)
}

func (p *Proxy) request(r *http.Request, endpoint string) error {
	method := r.Method
	if (endpoint == "/_ping" && (method == "GET" || method == "HEAD")) || (method == "GET" && (endpoint == "/version" || endpoint == "/info")) {
		return nil
	}
	if endpoint == "/grpc" {
		return errors.New("shared test checks require the Engine build API; use DOCKER_BUILDKIT=0 and COMPOSE_BAKE=false")
	}
	if endpoint == "/session" && method == "POST" {
		return errors.New("BuildKit sessions are unavailable on the scoped test daemon; use DOCKER_BUILDKIT=0")
	}
	if endpoint == "/build" && method == "POST" {
		return p.build(r)
	}
	if endpoint == "/events" && method == "GET" {
		return p.filters(r, false)
	}
	if endpoint == "/containers/json" && method == "GET" {
		return p.filters(r, true)
	}
	if endpoint == "/containers/create" && method == "POST" {
		return p.createContainer(r)
	}
	if endpoint == "/containers/prune" && method == "POST" {
		return p.filters(r, true)
	}
	if strings.HasPrefix(endpoint, "/containers/") {
		return p.containerRequest(r, endpoint)
	}
	if strings.HasPrefix(endpoint, "/exec/") {
		parts := strings.Split(strings.TrimPrefix(endpoint, "/exec/"), "/")
		if len(parts) != 2 || !objectID.MatchString(parts[0]) || (parts[1] != "json" && parts[1] != "start" && parts[1] != "resize") {
			return errors.New("unsupported exec request")
		}
		data, err := p.raw(r.Context(), "GET", "/exec/"+parts[0]+"/json", nil)
		if err != nil {
			return errors.New("exec is unavailable in this test session")
		}
		if _, err = p.owned(r.Context(), "containers", stringValue(data["ContainerID"])); err != nil {
			return err
		}
		return nil
	}
	if endpoint == "/networks" && method == "GET" {
		return p.filters(r, true)
	}
	if endpoint == "/networks/create" && method == "POST" {
		return p.createNetwork(r)
	}
	if endpoint == "/networks/prune" && method == "POST" {
		return p.filters(r, true)
	}
	if strings.HasPrefix(endpoint, "/networks/") {
		return p.networkRequest(r, endpoint)
	}
	if endpoint == "/volumes" && method == "GET" {
		return p.filters(r, true)
	}
	if endpoint == "/volumes"+"/create" && method == "POST" {
		return p.createVolume(r)
	}
	if endpoint == "/volumes"+"/prune" && method == "POST" {
		return p.filters(r, true)
	}
	if strings.HasPrefix(endpoint, "/volumes/") && (method == "GET" || method == "DELETE") {
		name := strings.TrimPrefix(endpoint, "/volumes/")
		if strings.Contains(name, "/") {
			return errors.New("unsupported volume request")
		}
		mapped, err := p.owned(r.Context(), "volumes", name)
		if err != nil {
			return err
		}
		replaceEndpoint(r, endpoint, "/volumes/"+mapped)
		return nil
	}
	if strings.HasPrefix(endpoint, "/images") {
		return p.imageRequest(r, endpoint)
	}
	if strings.HasPrefix(endpoint, "/distribution/") && method == "GET" {
		return nil
	}
	return fmt.Errorf("Docker API operation %s %s is unavailable in an isolated test session", method, endpoint)
}

func replaceEndpoint(r *http.Request, old, next string) {
	r.URL.Path = strings.TrimSuffix(r.URL.Path, old) + next
	r.URL.RawPath = ""
}

func (p *Proxy) scoped(name string) string {
	if strings.HasPrefix(name, p.config.Session+"-") {
		return name
	}
	return p.config.Session + "-" + name
}
func (p *Proxy) original(name string) string { return strings.TrimPrefix(name, p.config.Session+"-") }
func stringValue(value any) string           { s, _ := value.(string); return s }

func anonymousName() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return "anonymous-" + hex.EncodeToString(token[:]), nil
}
func object(value any) map[string]any {
	m, _ := value.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func array(value any) []any { a, _ := value.([]any); return a }

func decodeBody(r *http.Request) (map[string]any, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	r.Body.Close()
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("Docker configuration exceeds 1 MiB")
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil || value == nil {
		return nil, errors.New("invalid Docker configuration")
	}
	if err := validateRequestFields(value); err != nil {
		return nil, err
	}
	return value, nil
}

// Engine's struct decoder matches field names case-insensitively. Reject
// alternate spellings of policy fields before map-based validation, including
// variants that would override a canonical field after the request is encoded.
// Only schema objects are checked; labels, options and resource-name maps keep
// their user-defined keys.
func validateRequestFields(body map[string]any) error {
	check := func(value map[string]any, fields ...string) error {
		for key := range value {
			for _, field := range fields {
				if key != field && strings.EqualFold(key, field) {
					return fmt.Errorf("Docker policy field %s must use its canonical spelling", field)
				}
			}
		}
		return nil
	}
	if err := check(body, "HostConfig", "NetworkingConfig", "Image", "Labels", "Volumes", "ExposedPorts", "Name", "Driver", "DriverOpts", "Options", "IPAM", "Container", "EndpointConfig", "Privileged"); err != nil {
		return err
	}
	host := object(body["HostConfig"])
	if err := check(host, "Privileged", "Devices", "DeviceRequests", "DeviceCgroupRules", "PidMode", "IpcMode", "UsernsMode", "CgroupnsMode", "CapAdd", "NetworkMode", "Links", "VolumesFrom", "Binds", "Mounts", "PortBindings", "PublishAllPorts"); err != nil {
		return err
	}
	for _, value := range array(host["Mounts"]) {
		mount := object(value)
		if err := check(mount, "Type", "Source", "Target", "BindOptions", "VolumeOptions"); err != nil {
			return err
		}
		if err := check(object(mount["BindOptions"]), "Propagation"); err != nil {
			return err
		}
		if err := check(object(mount["VolumeOptions"]), "DriverConfig", "Subpath"); err != nil {
			return err
		}
	}
	if err := check(object(body["IPAM"]), "Driver", "Config", "Options"); err != nil {
		return err
	}
	if err := check(object(body["EndpointConfig"]), "IPAMConfig"); err != nil {
		return err
	}
	networking := object(body["NetworkingConfig"])
	if err := check(networking, "EndpointsConfig"); err != nil {
		return err
	}
	for _, endpoint := range object(networking["EndpointsConfig"]) {
		if err := check(object(endpoint), "IPAMConfig"); err != nil {
			return err
		}
	}
	return nil
}
func setBody(r *http.Request, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.Header.Set("Content-Type", "application/json")
	return nil
}
func (p *Proxy) labels(value map[string]any) {
	labels := object(value["Labels"])
	labels[SessionLabel] = p.config.Session
	// Explicit values override labels inherited from an image. Deleting these
	// keys would let an image impersonate controller-owned infrastructure.
	labels[InternalLabel] = "false"
	labels["io.sdlc.test-owner"] = ""
	value["Labels"] = labels
}
func (p *Proxy) filters(r *http.Request, names bool) error {
	query := r.URL.Query()
	filters := map[string][]string{}
	if raw := query.Get("filters"); raw != "" {
		var entries map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &entries) != nil {
			return errors.New("invalid Docker filters")
		}
		for key, value := range entries {
			var values []string
			if json.Unmarshal(value, &values) != nil {
				var legacy map[string]bool
				if json.Unmarshal(value, &legacy) != nil {
					return errors.New("invalid Docker filters")
				}
				for item, enabled := range legacy {
					if enabled {
						values = append(values, item)
					}
				}
			}
			filters[key] = values
		}
	}
	filters["label"] = append(filters["label"], SessionLabel+"="+p.config.Session)
	if names {
		for i, name := range filters["name"] {
			if strings.HasPrefix(name, "^/") {
				filters["name"][i] = "^/" + p.scoped(strings.TrimPrefix(name, "^/"))
			} else if strings.HasPrefix(name, "^") {
				filters["name"][i] = "^" + p.scoped(strings.TrimPrefix(name, "^"))
			} else {
				filters["name"][i] = p.scoped(name)
			}
		}
	}
	data, _ := json.Marshal(filters)
	query.Set("filters", string(data))
	r.URL.RawQuery = query.Encode()
	return nil
}

func (p *Proxy) raw(ctx context.Context, method, endpoint string, body any) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, "http://docker"+endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if response.StatusCode >= 300 {
		return nil, &apiError{code: response.StatusCode}
	}
	if err != nil || len(data) > 8<<20 {
		return nil, errors.New("Docker resource operation failed")
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return nil, errors.New("invalid Docker resource response")
	}
	return value, nil
}

func (p *Proxy) owned(ctx context.Context, kind, name string) (string, error) {
	if name == "" {
		return "", errors.New("resource is outside this test session")
	}
	candidates := []string{p.scoped(name)}
	if objectID.MatchString(name) {
		candidates = []string{name, p.scoped(name)}
	}
	var last error
	for _, mapped := range candidates {
		endpoint := "/" + kind + "/" + url.PathEscape(mapped)
		if kind == "containers" {
			endpoint += "/json"
		}
		data, err := p.raw(ctx, "GET", endpoint, nil)
		if err != nil {
			last = err
			var api *apiError
			if errors.As(err, &api) && api.code == http.StatusNotFound {
				continue
			}
			return "", err
		}
		labels := object(data["Labels"])
		if kind == "containers" {
			labels = object(object(data["Config"])["Labels"])
		}
		if stringValue(labels[SessionLabel]) != p.config.Session || stringValue(labels[InternalLabel]) == "true" {
			last = errors.New("resource is outside this test session")
			continue
		}
		if id := stringValue(data["Id"]); id != "" {
			return id, nil
		}
		return mapped, nil
	}
	return "", last
}

type apiError struct{ code int }

func (e *apiError) Error() string {
	if e.code == http.StatusNotFound {
		return "Docker resource not found in this session"
	}
	if e.code == http.StatusConflict {
		return "Docker resource is still in use"
	}
	return "Docker resource operation failed"
}
