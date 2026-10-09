package testproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type binding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

func (p *Proxy) containerRequest(r *http.Request, endpoint string) error {
	parts := strings.Split(strings.TrimPrefix(endpoint, "/containers/"), "/")
	if len(parts) > 2 {
		return errors.New("unsupported container operation")
	}
	name, err := p.owned(r.Context(), "containers", parts[0])
	if err != nil {
		return err
	}
	operation := ""
	if len(parts) == 2 {
		operation = parts[1]
	}
	allowed := map[string]string{"json": "GET", "logs": "GET", "stats": "GET", "top": "GET", "changes": "GET", "export": "GET", "start": "POST", "stop": "POST", "restart": "POST", "kill": "POST", "pause": "POST", "unpause": "POST", "wait": "POST", "attach": "POST", "resize": "POST", "exec": "POST"}
	if operation == "archive" && (r.Method == "GET" || r.Method == "HEAD" || r.Method == "PUT") {
	} else if operation == "" && r.Method == "DELETE" {
	} else if allowed[operation] != r.Method {
		return errors.New("unsupported container operation")
	}
	if operation == "exec" {
		body, err := decodeBody(r)
		if err != nil {
			return err
		}
		if body["Privileged"] == true {
			return errors.New("privileged exec is unavailable in shared checks")
		}
		if err = setBody(r, body); err != nil {
			return err
		}
	}
	replaceEndpoint(r, endpoint, "/containers/"+name+func() string {
		if operation != "" {
			return "/" + operation
		}
		return ""
	}())
	return nil
}

func (p *Proxy) networkRef(ctx context.Context, value string) (string, error) {
	switch value {
	case "", "default", "bridge":
		return p.config.Session + "-default", nil
	case "none":
		return "none", nil
	case "host":
		return "container:" + p.config.ProxyID, nil
	}
	if strings.HasPrefix(value, "container:") {
		id, err := p.owned(ctx, "containers", strings.TrimPrefix(value, "container:"))
		return "container:" + id, err
	}
	return p.owned(ctx, "networks", value)
}

func (p *Proxy) bindSource(source string) (string, error) {
	if source == "/run/sdlc/docker.sock" || source == "/var/run/docker.sock" {
		return p.config.SocketSource, nil
	}
	if source != "/workspace" && !strings.HasPrefix(source, "/workspace/") {
		return "", errors.New("bind mounts must stay inside this check workspace")
	}
	clean := filepath.Clean(source)
	if clean != source {
		return "", errors.New("bind mount contains traversal")
	}
	localRoot := p.config.LocalWorkspace
	localRoot, err := filepath.EvalSymlinks(localRoot)
	if err != nil {
		return "", errors.New("check workspace mount is unavailable")
	}
	current := localRoot + strings.TrimPrefix(source, "/workspace")
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", errors.New("bind mount contains an unresolved workspace link")
			}
			if resolved != localRoot && !strings.HasPrefix(resolved, localRoot+"/") {
				return "", errors.New("bind mount leaves this check workspace")
			}
			// Docker resolves sources in its own filesystem. Forward the canonical
			// workspace suffix, never a link that could resolve differently there.
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			relative, err := filepath.Rel(localRoot, resolved)
			if err != nil {
				return "", errors.New("cannot resolve workspace bind mount")
			}
			return filepath.Join(p.config.Workspace, relative), nil
		}
		if !errors.Is(err, os.ErrNotExist) || current == localRoot {
			return "", errors.New("check workspace mount is unavailable")
		}
		missing = append(missing, filepath.Base(current))
		current = filepath.Dir(current)
	}
}

func (p *Proxy) volume(ctx context.Context, name string) (string, error) {
	if name == "" {
		var err error
		name, err = anonymousName()
		if err != nil {
			return "", err
		}
	}
	mapped := p.scoped(name)
	if strings.ContainsAny(name, "/\\:\x00") || strings.HasPrefix(name, "-") {
		return "", errors.New("invalid session volume name")
	}
	body := map[string]any{"Name": mapped, "Driver": "local", "Labels": map[string]string{SessionLabel: p.config.Session}}
	data, err := p.raw(ctx, "POST", "/volumes"+"/create", body)
	if err != nil {
		return "", err
	}
	if stringValue(object(data["Labels"])[SessionLabel]) != p.config.Session {
		return "", errors.New("volume ownership differs from this session")
	}
	return mapped, nil
}

func (p *Proxy) createContainer(r *http.Request) error {
	body, err := decodeBody(r)
	if err != nil {
		return err
	}
	p.labels(body)
	host := object(body["HostConfig"])
	for _, key := range []string{"Privileged"} {
		if host[key] == true {
			return fmt.Errorf("%s is unavailable in shared checks", key)
		}
	}
	for _, key := range []string{"Devices", "DeviceRequests", "DeviceCgroupRules"} {
		if len(array(host[key])) != 0 {
			return errors.New("host devices are unavailable in shared checks")
		}
	}
	for _, key := range []string{"PidMode", "IpcMode", "UsernsMode", "CgroupnsMode"} {
		if value := stringValue(host[key]); value == "host" || strings.HasPrefix(value, "container:") {
			return errors.New("shared host namespaces are unavailable in checks")
		}
	}
	for _, cap := range array(host["CapAdd"]) {
		if value := strings.ToUpper(stringValue(cap)); value == "ALL" || value == "SYS_ADMIN" || value == "SYS_MODULE" {
			return errors.New("host administration capabilities are unavailable in shared checks")
		}
	}
	mode, err := p.networkRef(r.Context(), stringValue(host["NetworkMode"]))
	if err != nil {
		return err
	}
	host["NetworkMode"] = mode
	for _, field := range []string{"Links", "VolumesFrom"} {
		values := array(host[field])
		for i, raw := range values {
			parts := strings.SplitN(stringValue(raw), ":", 2)
			id, err := p.owned(r.Context(), "containers", parts[0])
			if err != nil {
				return err
			}
			if len(parts) == 2 {
				id += ":" + parts[1]
			}
			values[i] = id
		}
		host[field] = values
	}
	occupied := map[string]bool{}
	binds := array(host["Binds"])
	for i, raw := range binds {
		parts := strings.SplitN(stringValue(raw), ":", 3)
		if len(parts) < 2 || parts[1] == "" {
			return errors.New("invalid bind mount")
		}
		occupied[parts[1]] = true
		var source string
		if strings.HasPrefix(parts[0], "/") {
			source, err = p.bindSource(parts[0])
		} else {
			source, err = p.volume(r.Context(), parts[0])
		}
		if err != nil {
			return err
		}
		parts[0] = source
		if len(parts) == 3 && (strings.Contains(parts[2], "shared") || strings.Contains(parts[2], "slave")) {
			return errors.New("shared mount propagation is unavailable in checks")
		}
		binds[i] = strings.Join(parts, ":")
	}
	host["Binds"] = binds
	mounts := array(host["Mounts"])
	for _, raw := range mounts {
		mount := object(raw)
		target := stringValue(mount["Target"])
		occupied[target] = true
		switch stringValue(mount["Type"]) {
		case "bind":
			if propagation := stringValue(object(mount["BindOptions"])["Propagation"]); propagation != "" && propagation != "rprivate" && propagation != "private" {
				return errors.New("shared mount propagation is unavailable in checks")
			}
			mount["Source"], err = p.bindSource(stringValue(mount["Source"]))
		case "volume":
			options := object(mount["VolumeOptions"])
			if len(object(options["DriverConfig"])) != 0 || options["Subpath"] != nil {
				return errors.New("volume driver options are unavailable in checks")
			}
			mount["Source"], err = p.volume(r.Context(), stringValue(mount["Source"]))
		case "tmpfs":
		default:
			return errors.New("unsupported check mount type")
		}
		if err != nil {
			return err
		}
	}
	image, metadata, err := p.imageForSession(r.Context(), stringValue(body["Image"]))
	if err != nil {
		return err
	}
	body["Image"] = image
	imageConfig := object(metadata["Config"])
	volumes := object(body["Volumes"])
	for target := range object(imageConfig["Volumes"]) {
		volumes[target] = map[string]any{}
	}
	for target := range volumes {
		if !occupied[target] {
			name, err := p.volume(r.Context(), "")
			if err != nil {
				return err
			}
			mounts = append(mounts, map[string]any{"Type": "volume", "Source": name, "Target": target})
		}
	}
	body["Volumes"] = map[string]any{}
	host["Mounts"] = mounts
	var requested map[string][]binding
	if raw := host["PortBindings"]; raw != nil {
		data, _ := json.Marshal(raw)
		if json.Unmarshal(data, &requested) != nil {
			return errors.New("invalid published ports")
		}
	}
	if host["PublishAllPorts"] == true {
		if requested == nil {
			requested = map[string][]binding{}
		}
		for _, exposed := range []map[string]any{object(imageConfig["ExposedPorts"]), object(body["ExposedPorts"])} {
			for port := range exposed {
				if len(requested[port]) == 0 {
					requested[port] = []binding{{}}
				}
			}
		}
		host["PublishAllPorts"] = false
	}
	if strings.HasPrefix(mode, "container:") || mode == "none" {
		requested = nil
	}
	host["PortBindings"] = map[string]any{}
	exposed := object(body["ExposedPorts"])
	for port := range requested {
		exposed[port] = map[string]any{}
	}
	body["ExposedPorts"] = exposed
	networking := object(body["NetworkingConfig"])
	endpoints := object(networking["EndpointsConfig"])
	transformed := map[string]any{}
	for name, raw := range endpoints {
		endpoint := object(raw)
		if len(object(endpoint["IPAMConfig"])) != 0 {
			return errors.New("static network addresses are unavailable in parallel checks")
		}
		mapped, err := p.networkRef(r.Context(), name)
		if err != nil {
			return err
		}
		if mapped == "none" || strings.HasPrefix(mapped, "container:") {
			return errors.New("invalid explicit network endpoint")
		}
		transformed[mapped] = endpoint
	}
	networking["EndpointsConfig"] = transformed
	body["NetworkingConfig"] = networking
	body["HostConfig"] = host
	query := r.URL.Query()
	name := query.Get("name")
	if name == "" {
		name = fmt.Sprintf("container-%d", p.sequence.Add(1))
	}
	if strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, "-") {
		return errors.New("invalid session container name")
	}
	query.Set("name", p.scoped(name))
	r.URL.RawQuery = query.Encode()
	r = storeRequest(r, requested)
	return setBody(r, body)
}

type requestKey struct{}

func storeRequest(r *http.Request, ports map[string][]binding) *http.Request {
	*r = *r.WithContext(context.WithValue(r.Context(), requestKey{}, ports))
	return r
}
