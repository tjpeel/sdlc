package testproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (p *Proxy) createNetwork(r *http.Request) error {
	body, err := decodeBody(r)
	if err != nil {
		return err
	}
	name := stringValue(body["Name"])
	if name == "" || strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, "-") {
		return errors.New("invalid session network name")
	}
	if driver := stringValue(body["Driver"]); driver != "" && driver != "bridge" {
		return errors.New("parallel checks support bridge networks")
	}
	ipam := object(body["IPAM"])
	if len(array(ipam["Config"])) != 0 || len(object(ipam["Options"])) != 0 || (ipam["Driver"] != nil && ipam["Driver"] != "default") {
		return errors.New("static network addressing is unavailable in parallel checks")
	}
	options := object(body["Options"])
	for key := range options {
		if strings.HasPrefix(key, "com.docker.network.bridge.") {
			return errors.New("host bridge options are unavailable in parallel checks")
		}
	}
	p.labels(body)
	body["Name"] = p.scoped(name)
	return setBody(r, body)
}

func (p *Proxy) networkRequest(r *http.Request, endpoint string) error {
	parts := strings.Split(strings.TrimPrefix(endpoint, "/networks/"), "/")
	if len(parts) > 2 {
		return errors.New("unsupported network operation")
	}
	id, err := p.owned(r.Context(), "networks", parts[0])
	if err != nil {
		return err
	}
	operation := ""
	if len(parts) == 2 {
		operation = parts[1]
	}
	if operation == "" && (r.Method == "GET" || r.Method == "DELETE") {
		if r.Method == "DELETE" {
			metadata, err := p.raw(r.Context(), "GET", "/networks/"+id, nil)
			if err != nil {
				return err
			}
			for container := range object(metadata["Containers"]) {
				if !strings.HasPrefix(container, p.config.ProxyID) {
					return &apiError{code: http.StatusConflict}
				}
			}
			_, err = p.raw(r.Context(), "POST", "/networks/"+id+"/disconnect", map[string]any{"Container": p.config.ProxyID, "Force": true})
			if err != nil {
				return err
			}
		}
	} else if (operation == "connect" || operation == "disconnect") && r.Method == "POST" {
		body, err := decodeBody(r)
		if err != nil {
			return err
		}
		container, err := p.owned(r.Context(), "containers", stringValue(body["Container"]))
		if err != nil {
			return err
		}
		if len(object(object(body["EndpointConfig"])["IPAMConfig"])) != 0 {
			return errors.New("static network addresses are unavailable in parallel checks")
		}
		body["Container"] = container
		if err = setBody(r, body); err != nil {
			return err
		}
	} else {
		return errors.New("unsupported network operation")
	}
	replaceEndpoint(r, endpoint, "/networks/"+id+func() string {
		if operation != "" {
			return "/" + operation
		}
		return ""
	}())
	return nil
}

func (p *Proxy) createVolume(r *http.Request) error {
	body, err := decodeBody(r)
	if err != nil {
		return err
	}
	if driver := stringValue(body["Driver"]); driver != "" && driver != "local" {
		return errors.New("parallel checks support local named volumes")
	}
	if len(object(body["DriverOpts"])) != 0 {
		return errors.New("host volume driver options are unavailable in parallel checks")
	}
	name := stringValue(body["Name"])
	if name == "" {
		name, err = anonymousName()
		if err != nil {
			return err
		}
	}
	if strings.ContainsAny(name, "/\\:\x00") || strings.HasPrefix(name, "-") {
		return errors.New("invalid session volume name")
	}
	p.labels(body)
	body["Name"] = p.scoped(name)
	return setBody(r, body)
}

func (p *Proxy) imageAlias(name string) string {
	if !strings.Contains(name, "@") && !strings.Contains(strings.Split(name, "/")[len(strings.Split(name, "/"))-1], ":") {
		name += ":latest"
	}
	key := sha256.Sum256([]byte(name))
	alias := "sdlc-test/" + p.config.Session + ":" + hex.EncodeToString(key[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	p.images[name] = alias
	p.imageNames[alias] = name
	if strings.HasSuffix(name, ":latest") {
		p.images[strings.TrimSuffix(name, ":latest")] = alias
	}
	return alias
}

func (p *Proxy) imageRequest(r *http.Request, endpoint string) error {
	if (endpoint == "/images/json" || endpoint == "/images/search") && r.Method == "GET" {
		return nil
	}
	if endpoint == "/images/create" && r.Method == "POST" {
		if r.URL.Query().Get("fromImage") == "" || r.URL.Query().Get("fromSrc") != "" {
			return errors.New("image imports are controlled by SDLC; pull a named image instead")
		}
		if ReservedImageReference(r.URL.Query().Get("fromImage")) {
			return errors.New("image repository is reserved for controller resources")
		}
		return nil
	}
	if !strings.HasPrefix(endpoint, "/images/") {
		return errors.New("global image and build-cache mutation is unavailable in shared checks")
	}
	name := strings.TrimPrefix(endpoint, "/images/")
	operation := ""
	for _, suffix := range []string{"/json", "/history", "/tag", "/push"} {
		if strings.HasSuffix(name, suffix) {
			name = strings.TrimSuffix(name, suffix)
			operation = suffix
			break
		}
	}
	if name == "prune" || name == "load" || name == "get" {
		return errors.New("global image and build-cache mutation is unavailable in shared checks")
	}
	mapped, metadata, err := p.imageForSession(r.Context(), name)
	if err != nil {
		return err
	}
	owner := stringValue(object(object(metadata["Config"])["Labels"])[SessionLabel])
	if r.Method == "GET" && (operation == "/json" || operation == "/history") {
		replaceEndpoint(r, endpoint, "/images/"+mapped+operation)
		return nil
	}
	if operation == "/tag" && r.Method == "POST" && owner == p.config.Session {
		query := r.URL.Query()
		name := query.Get("repo")
		if tag := query.Get("tag"); tag != "" {
			name += ":" + tag
		}
		alias := p.imageAlias(name)
		parts := strings.SplitN(alias, ":", 2)
		query.Set("repo", parts[0])
		query.Set("tag", parts[1])
		r.URL.RawQuery = query.Encode()
		replaceEndpoint(r, endpoint, "/images/"+mapped+"/tag")
		return nil
	}
	if operation == "" && r.Method == "DELETE" && owner == p.config.Session && strings.HasPrefix(mapped, "sdlc-test/"+p.config.Session+":") {
		// Remove the caller's tag while retaining an immutable reference for warm
		// build reuse. Shared cache deletion is a separate controller action.
		id := stringValue(metadata["Id"])
		if !strings.HasPrefix(id, "sha256:") || len(id) != 71 {
			return errors.New("invalid cached image identity")
		}
		_, err := p.raw(r.Context(), "POST", "/images/"+id+"/tag?repo=sdlc-test-cache&tag="+strings.TrimPrefix(id, "sha256:"), nil)
		if err != nil {
			return err
		}
		replaceEndpoint(r, endpoint, "/images/"+mapped)
		return nil
	}
	return errors.New("shared image deletion, publication and global pruning are unavailable in checks")
}

func (p *Proxy) response(response *http.Response) error {
	if response.StatusCode >= 300 {
		return nil
	}
	r := response.Request
	endpoint := apiVersion.ReplaceAllString(r.URL.Path, "")
	if strings.HasPrefix(endpoint, "/containers/") && (strings.HasSuffix(endpoint, "/start") || strings.HasSuffix(endpoint, "/restart")) {
		id := strings.Split(strings.TrimPrefix(endpoint, "/containers/"), "/")[0]
		if err := p.startForwards(r.Context(), id); err != nil {
			p.raw(r.Context(), "POST", "/containers/"+id+"/stop?t=1", nil)
			p.stopForwards(id)
			return err
		}
		return nil
	}
	if strings.HasPrefix(endpoint, "/containers/") && (r.Method == "DELETE" || strings.HasSuffix(endpoint, "/stop")) {
		p.stopForwards(strings.Split(strings.TrimPrefix(endpoint, "/containers/"), "/")[0])
		return nil
	}
	jsonResponse := strings.Contains(response.Header.Get("Content-Type"), "application/json") && r.Method == "GET" &&
		(strings.HasSuffix(endpoint, "/json") || endpoint == "/networks" || strings.HasPrefix(endpoint, "/networks/") || strings.HasPrefix(endpoint, "/volumes"))
	jsonResponse = jsonResponse || endpoint == "/containers/create" || endpoint == "/networks/create" || endpoint == "/volumes"+"/create"
	if !jsonResponse {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	response.Body.Close()
	if err != nil || len(data) > 8<<20 {
		return errors.New("Docker response exceeds its bound")
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return errors.New("invalid Docker response")
	}
	if endpoint == "/containers/create" {
		id := stringValue(object(value)["Id"])
		bindings, _ := r.Context().Value(requestKey{}).(map[string][]binding)
		p.mu.Lock()
		p.requested[id] = bindings
		p.mu.Unlock()
	}
	if endpoint == "/networks/create" {
		id := stringValue(object(value)["Id"])
		if !objectID.MatchString(id) {
			return errors.New("invalid session network identity")
		}
		if _, err := p.raw(r.Context(), "POST", "/networks/"+id+"/connect", map[string]any{"Container": p.config.ProxyID}); err != nil {
			return err
		}
	}
	value = p.rewriteResponse(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	response.Body = io.NopCloser(strings.NewReader(string(encoded)))
	response.ContentLength = int64(len(encoded))
	response.Header.Del("Content-Length")
	return nil
}

func (p *Proxy) rewriteResponse(value any) any {
	switch node := value.(type) {
	case []any:
		out := make([]any, 0, len(node))
		for _, child := range node {
			item := object(child)
			labels := object(item["Labels"])
			if owner := stringValue(labels[SessionLabel]); owner != "" && owner != p.config.Session {
				continue
			}
			if stringValue(labels[InternalLabel]) == "true" {
				continue
			}
			if _, image := item["RepoTags"]; image && stringValue(labels[SessionLabel]) == "" && !sharedBaseImage(item) {
				continue
			}
			out = append(out, p.rewriteResponse(child))
		}
		return out
	case map[string]any:
		for key, child := range node {
			switch key {
			case "Image":
				node[key] = p.originalImage(stringValue(child))
			case "RepoTags":
				var tags []any
				for _, name := range array(child) {
					tag := stringValue(name)
					if imageRepository(tag) == "sdlc-test-cache" || imageRepository(tag) == "sdlc-test-base" {
						continue
					}
					logical := p.originalImage(tag)
					if strings.HasPrefix(logical, "sdlc-test/") {
						continue
					}
					tags = append(tags, logical)
				}
				node[key] = tags
			case "Containers":
				containers := object(child)
				for id := range containers {
					if strings.HasPrefix(id, p.config.ProxyID) {
						delete(containers, id)
					}
				}
				node[key] = p.rewriteResponse(containers)
			case "Name":
				if name := stringValue(child); name != "" {
					node[key] = strings.TrimPrefix(name, "/")
					node[key] = p.original(stringValue(node[key]))
					if strings.HasPrefix(name, "/") {
						node[key] = "/" + stringValue(node[key])
					}
				}
			case "Names":
				for i, name := range array(child) {
					names := array(child)
					names[i] = "/" + p.original(strings.TrimPrefix(stringValue(name), "/"))
				}
			default:
				node[key] = p.rewriteResponse(child)
			}
		}
		if id := stringValue(node["Id"]); id != "" && node["NetworkSettings"] != nil {
			p.mu.Lock()
			forwards := append([]*forward(nil), p.ports[id]...)
			p.mu.Unlock()
			mapped := map[string][]binding{}
			for _, f := range forwards {
				mapped[f.containerPort] = append(mapped[f.containerPort], binding{HostIP: f.hostIP, HostPort: f.port})
			}
			object(node["NetworkSettings"])["Ports"] = mapped
			object(node["HostConfig"])["PortBindings"] = mapped
			if _, summary := node["Ports"].([]any); summary {
				var ports []any
				for _, f := range forwards {
					parts := strings.Split(f.containerPort, "/")
					private, _ := strconv.Atoi(parts[0])
					public, _ := strconv.Atoi(f.port)
					ports = append(ports, map[string]any{"IP": f.hostIP, "PrivatePort": private, "PublicPort": public, "Type": parts[1]})
				}
				node["Ports"] = ports
			}
			networks := object(object(node["NetworkSettings"])["Networks"])
			originals := map[string]any{}
			for name, config := range networks {
				originals[p.original(name)] = config
			}
			object(node["NetworkSettings"])["Networks"] = originals
			if object(node["HostConfig"])["NetworkMode"] == "container:"+p.config.ProxyID {
				object(node["HostConfig"])["NetworkMode"] = "host"
			}
		}
		return node
	}
	return value
}

func (p *Proxy) originalImage(alias string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if name := p.imageNames[alias]; name != "" {
		return name
	}
	return alias
}
