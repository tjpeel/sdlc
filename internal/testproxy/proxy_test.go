package testproxy

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const exampleSession = "sdlc-check-aaaaaaaaaaaaaaaaaaaaaaaa"
const exampleProxy = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type engineProbe struct {
	mu            sync.Mutex
	calls         []string
	bodies        []map[string]any
	typedCreates  []engineContainerConfig
	volumes       []map[string]any
	owner         string
	internal      bool
	missingImage  bool
	networkIP     string
	imageLabels   map[string]string
	imageTags     []string
	privateImages map[string]string
}

// Engine decodes request structs, so field matching is case-insensitive. Keep
// that behaviour at this boundary rather than validating only decoded maps.
type engineContainerConfig struct {
	Image, Hostname string
	Labels          map[string]string
	HostConfig      struct {
		Privileged  bool
		Binds       []string
		NetworkMode string
	}
}

func (e *engineProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "POST" && r.URL.Path == "/volumes"+"/create" {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		e.volumes = append(e.volumes, body)
		json.NewEncoder(w).Encode(body)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/containers/create" {
		data, _ := io.ReadAll(r.Body)
		var typed engineContainerConfig
		json.Unmarshal(data, &typed)
		if typed.Labels == nil {
			typed.Labels = map[string]string{}
		}
		for name, value := range e.imageLabels {
			if _, present := typed.Labels[name]; !present {
				typed.Labels[name] = value
			}
		}
		e.typedCreates = append(e.typedCreates, typed)
		var body map[string]any
		json.Unmarshal(data, &body)
		body["requested_name"] = r.URL.Query().Get("name")
		e.bodies = append(e.bodies, body)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"Id": strings.Repeat("c", 64)})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/images/") {
		if e.missingImage {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"message": "not found"})
			return
		}
		labels, tags := e.imageLabels, e.imageTags
		if owner, private := e.privateImages[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/images/"), "/json")]; private {
			labels, tags = map[string]string{SessionLabel: owner}, nil
		}
		json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:" + strings.Repeat("d", 64), "RepoTags": tags, "Config": map[string]any{"Labels": labels, "Volumes": map[string]any{"/data": map[string]any{}}}})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/containers/") {
		labels := map[string]string{SessionLabel: e.owner}
		if e.internal {
			labels[InternalLabel] = "true"
		}
		json.NewEncoder(w).Encode(map[string]any{"Id": strings.Repeat("c", 64), "Name": "/" + e.owner + "-fixed", "Config": map[string]any{"Labels": labels}, "NetworkSettings": map[string]any{"Ports": map[string]any{}, "Networks": map[string]any{"session": map[string]string{"IPAddress": e.networkIP}}}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{})
}

func proxyFixture(t *testing.T, owner string) (*Proxy, *engineProbe) {
	t.Helper()
	e := &engineProbe{owner: owner, imageTags: []string{"public:1"}}
	root := t.TempDir()
	p, err := New(Config{Session: exampleSession, ProxyID: exampleProxy, Socket: "unused.sock", Workspace: "/sdlc/workspaces/" + exampleSession, SocketSource: "/sdlc/sockets/" + exampleSession + "/docker.sock", LocalWorkspace: root})
	if err != nil {
		t.Fatal(err)
	}
	transport := engineTransport{e}
	p.client.Transport, p.reverse.Transport = transport, transport
	t.Cleanup(func() { p.Close() })
	return p, e
}

type engineTransport struct{ handler http.Handler }

func (e engineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	response := w.Result()
	response.Request = r
	return response, nil
}
func requestProxy(t *testing.T, p *Proxy, method, endpoint string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, endpoint, bytes.NewReader(data))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func TestContainerCreateNamespacesNamesMountsPortsAndImplicitVolumes(t *testing.T) {
	p, engine := proxyFixture(t, exampleSession)
	body := map[string]any{"Image": "public:1", "HostConfig": map[string]any{"Binds": []string{"/workspace:/source:ro", "/run/sdlc/docker.sock:/var/run/docker.sock"}, "PortBindings": map[string][]binding{"80/tcp": {{HostIP: "127.0.0.1", HostPort: "18080"}}}}}
	w := requestProxy(t, p, "POST", "/containers/create?name=fixed", body)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	created := engine.bodies[0]
	host := object(created["HostConfig"])
	if created["requested_name"] != exampleSession+"-fixed" || stringValue(object(created["Labels"])[SessionLabel]) != exampleSession {
		t.Fatal("container namespace missing")
	}
	if len(object(host["PortBindings"])) != 0 || host["NetworkMode"] != exampleSession+"-default" {
		t.Fatal("service published globally or default network shared")
	}
	binds := array(host["Binds"])
	if stringValue(binds[0]) != "/sdlc/workspaces/"+exampleSession+":/source:ro" || stringValue(binds[1]) != "/sdlc/sockets/"+exampleSession+"/docker.sock:/var/run/docker.sock" {
		t.Fatal("mount did not use owned workspace/session socket")
	}
	if len(engine.volumes) != 1 || stringValue(object(engine.volumes[0]["Labels"])[SessionLabel]) != exampleSession {
		t.Fatal("image anonymous volume not owned")
	}
	if len(p.requested[strings.Repeat("c", 64)]["80/tcp"]) != 1 {
		t.Fatal("published port metadata lost")
	}
}

func TestBindMountSourcesResolveOnlyInsideSessionWorkspace(t *testing.T) {
	for _, test := range []struct {
		name, source, expected string
		rejected               bool
	}{
		{name: "existing directory", source: "/workspace/safe", expected: "/safe"},
		{name: "missing descendants", source: "/workspace/safe/new/leaf", expected: "/safe/new/leaf"},
		{name: "absolute link inside workspace", source: "/workspace/absolute", expected: "/safe"},
		{name: "dangling absolute link", source: "/workspace/dangling", rejected: true},
		{name: "missing child of dangling link", source: "/workspace/dangling/child", rejected: true},
		{name: "dangling link chain", source: "/workspace/chain", rejected: true},
		{name: "link cycle", source: "/workspace/cycle", rejected: true},
		{name: "non-directory ancestor", source: "/workspace/file/child", rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, engine := proxyFixture(t, exampleSession)
			root := p.config.LocalWorkspace
			if err := os.Mkdir(filepath.Join(root, "safe"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			for name, target := range map[string]string{
				"absolute": filepath.Join(root, "safe"),
				"dangling": "/sdlc/templates",
				"chain":    "dangling",
				"cycle":    "cycle",
			} {
				if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			body := map[string]any{"Image": "public:1", "HostConfig": map[string]any{"Binds": []string{test.source + ":/source"}}}
			response := requestProxy(t, p, "POST", "/containers/create?name=bind-check", body)
			if test.rejected {
				if response.Code != http.StatusForbidden || len(engine.bodies) != 0 {
					t.Fatalf("unsafe bind reached daemon: status %d, creates %d, response %s", response.Code, len(engine.bodies), response.Body.String())
				}
				return
			}
			if response.Code != http.StatusCreated || len(engine.bodies) != 1 {
				t.Fatalf("workspace bind rejected: status %d, response %s", response.Code, response.Body.String())
			}
			binds := array(object(engine.bodies[0]["HostConfig"])["Binds"])
			if len(binds) != 1 || binds[0] != "/sdlc/workspaces/"+exampleSession+test.expected+":/source" {
				t.Fatalf("daemon bind retained an unresolved workspace path: %v", binds)
			}
		})
	}
}

func TestHostNetworkingUsesSessionNamespace(t *testing.T) {
	p, engine := proxyFixture(t, exampleSession)
	w := requestProxy(t, p, "POST", "/containers/create?name=host-tests", map[string]any{"Image": "public:1", "HostConfig": map[string]any{"NetworkMode": "host"}})
	if w.Code != 201 || object(engine.bodies[0]["HostConfig"])["NetworkMode"] != "container:"+exampleProxy {
		t.Fatal("host networking escaped session")
	}
}

func TestForeignAndInfrastructureObjectsCannotBeMutated(t *testing.T) {
	for _, internal := range []bool{false, true} {
		p, engine := proxyFixture(t, "sdlc-check-eeeeeeeeeeeeeeeeeeeeeeee")
		if internal {
			engine.owner = exampleSession
			engine.internal = true
		}
		for _, endpoint := range []string{"/containers/" + strings.Repeat("c", 64), "/containers/" + strings.Repeat("c", 64) + "/exec", "/containers/" + strings.Repeat("c", 64) + "/start"} {
			method := "POST"
			if strings.HasSuffix(endpoint, strings.Repeat("c", 64)) {
				method = "DELETE"
			}
			w := requestProxy(t, p, method, endpoint, map[string]any{"Cmd": []string{"true"}})
			if w.Code != 403 {
				t.Fatal("foreign resource accepted", endpoint)
			}
		}
		for _, call := range engine.calls {
			if !strings.HasPrefix(call, "GET ") {
				t.Fatal("forbidden mutation reached engine")
			}
		}
	}
}

func TestConfigurationBypassesAreRejected(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	for _, host := range []map[string]any{{"Privileged": true}, {"PidMode": "host"}, {"Devices": []any{map[string]string{"PathOnHost": "/dev/sda"}}}, {"Binds": []string{"/var/lib/docker:/data"}}, {"Mounts": []any{map[string]any{"Type": "volume", "Source": "escape", "Target": "/x", "VolumeOptions": map[string]any{"DriverConfig": map[string]any{"Name": "local", "Options": map[string]string{"device": "/"}}}}}}} {
		if w := requestProxy(t, p, "POST", "/containers/create", map[string]any{"Image": "public:1", "HostConfig": host}); w.Code != 403 {
			t.Fatal("accepted host configuration", host)
		}
	}
	for _, endpoint := range []string{"/grpc", "/session", "/build/prune", "/swarm/init", "/plugins/create"} {
		if requestProxy(t, p, "POST", endpoint, nil).Code != 403 {
			t.Fatal("accepted global operation", endpoint)
		}
	}
	if requestProxy(t, p, "POST", "/volumes"+"/create", map[string]any{"Name": "escape", "DriverOpts": map[string]string{"device": "/"}}).Code != 403 {
		t.Fatal("volume driver bypass")
	}
	if requestProxy(t, p, "POST", "/networks/create", map[string]any{"Name": "fixed", "IPAM": map[string]any{"Config": []any{map[string]string{"Subnet": "172.24.0.0/24"}}}}).Code != 403 {
		t.Fatal("static network collision not rejected")
	}
}

func TestDockerFieldCaseCannotBypassSessionPolicy(t *testing.T) {
	for _, test := range []struct {
		name, endpoint string
		body           map[string]any
	}{
		{name: "lowercase privilege", endpoint: "/containers/create", body: map[string]any{"Image": "public:1", "HostConfig": map[string]any{"privileged": true}}},
		{name: "host configuration collision", endpoint: "/containers/create", body: map[string]any{"Image": "public:1", "HostConfig": map[string]any{}, "hostconfig": map[string]any{"Privileged": true, "Binds": []string{"/run/sdlc/docker.sock:/raw"}, "NetworkMode": "host"}}},
		{name: "lowercase binds", endpoint: "/containers/create", body: map[string]any{"Image": "public:1", "HostConfig": map[string]any{"binds": []string{"/run/sdlc/docker.sock:/raw"}}}},
		{name: "lowercase network mode", endpoint: "/containers/create", body: map[string]any{"Image": "public:1", "HostConfig": map[string]any{"networkmode": "host"}}},
		{name: "label ownership collision", endpoint: "/containers/create", body: map[string]any{"Image": "public:1", "Labels": map[string]string{}, "labels": map[string]string{SessionLabel: "another-session"}}},
		{name: "mount source collision", endpoint: "/containers/create", body: map[string]any{"Image": "public:1", "HostConfig": map[string]any{"Mounts": []any{map[string]any{"Type": "bind", "Source": "/workspace", "source": "/sdlc/templates", "Target": "/data"}}}}},
		{name: "volume mount options", endpoint: "/containers/create", body: map[string]any{"Image": "public:1", "HostConfig": map[string]any{"Mounts": []any{map[string]any{"Type": "volume", "Source": "data", "Target": "/data", "volumeOptions": map[string]any{"DriverConfig": map[string]any{"Name": "local", "Options": map[string]string{"device": "/", "type": "none", "o": "bind"}}}}}}}},
		{name: "volume driver options", endpoint: "/volumes" + "/create", body: map[string]any{"Name": "data", "Driver": "local", "driverOpts": map[string]string{"device": "/", "type": "none", "o": "bind"}}},
		{name: "network addressing", endpoint: "/networks/create", body: map[string]any{"Name": "network", "IPAM": map[string]any{"config": []any{map[string]string{"Subnet": "172.24.0.0/24"}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, engine := proxyFixture(t, exampleSession)
			response := requestProxy(t, p, "POST", test.endpoint, test.body)
			if response.Code != http.StatusForbidden {
				t.Fatalf("case variant reached Engine typed decoder: status %d, configs %+v", response.Code, engine.typedCreates)
			}
			for _, call := range engine.calls {
				if !strings.HasPrefix(call, "GET ") {
					t.Fatalf("case variant caused daemon mutation: %s", call)
				}
			}
		})
	}
}

func TestUserLabelKeysKeepTheirCase(t *testing.T) {
	p, engine := proxyFixture(t, exampleSession)
	response := requestProxy(t, p, "POST", "/containers/create", map[string]any{"Image": "public:1", "Labels": map[string]string{"hostconfig": "project-label", "Privileged": "metadata"}})
	if response.Code != http.StatusCreated || engine.typedCreates[0].Labels["hostconfig"] != "project-label" || engine.typedCreates[0].Labels["Privileged"] != "metadata" {
		t.Fatal("user label keys were treated as Docker schema fields", response.Code)
	}
}

func TestInheritedImageLabelsCannotImpersonateController(t *testing.T) {
	p, engine := proxyFixture(t, exampleSession)
	engine.imageLabels = map[string]string{InternalLabel: "true", "io.sdlc.test-owner": "foreign-session", "project": "inherited"}
	response := requestProxy(t, p, "POST", "/containers/create", map[string]any{"Image": "public:1"})
	if response.Code != http.StatusCreated {
		t.Fatal(response.Code, response.Body.String())
	}
	labels := engine.typedCreates[0].Labels
	if labels[InternalLabel] != "false" || labels["io.sdlc.test-owner"] != "" || labels["project"] != "inherited" || labels[SessionLabel] != exampleSession {
		t.Fatal("inherited labels can affect controller cleanup", labels)
	}
}

func TestPrivateImagesAreUnavailableAcrossAllImageInputs(t *testing.T) {
	for _, test := range []struct {
		name, owner string
		tags        []string
	}{
		{name: "foreign final image", owner: "foreign-session", tags: []string{"public:1"}},
		{name: "ownerless intermediate"},
		{name: "forged shared marker", tags: []string{"sdlc-test-base:" + strings.Repeat("e", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, engine := proxyFixture(t, exampleSession)
			engine.imageLabels, engine.imageTags = map[string]string{SessionLabel: test.owner}, test.tags
			id := "sha256:" + strings.Repeat("d", 64)
			for _, request := range []struct {
				method, endpoint string
				body             any
			}{
				{"GET", "/images/" + id + "/json", nil},
				{"POST", "/containers/create", map[string]any{"Image": id}},
			} {
				if response := requestProxy(t, p, request.method, request.endpoint, request.body); response.Code != http.StatusForbidden {
					t.Fatal("private image exposed", request.endpoint, response.Code)
				}
			}
			for _, source := range []string{"FROM " + id + "\n", "FROM public:1\nCOPY --from=" + id + " /private /copied\n"} {
				if _, err := p.dockerfile(context.Background(), source, nil); err == nil {
					t.Fatal("private image accepted as build input", source)
				}
			}
			images := []any{map[string]any{"Id": id, "RepoTags": func() []any {
				var tags []any
				for _, tag := range test.tags {
					tags = append(tags, tag)
				}
				return tags
			}(), "Labels": map[string]any{SessionLabel: test.owner}}}
			if listed := p.rewriteResponse(images).([]any); len(listed) != 0 {
				t.Fatal("private intermediate exposed in image listing", listed)
			}
		})
	}
}

func TestReservedRegistryAliasesCannotBePulledOrBuilt(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	for _, name := range []string{"sdlc-test/foreign:tag", "docker.io/sdlc-test/foreign:tag", "sdlc-test-base:tag", "docker.io/library/sdlc-test-base:tag", "index.docker.io/library/sdlc-test-cache:tag"} {
		response := requestProxy(t, p, "POST", "/images/create?fromImage="+name, nil)
		if response.Code != http.StatusForbidden {
			t.Fatal("reserved registry namespace can overwrite local cache", name, response.Code)
		}
		if _, err := p.dockerfile(context.Background(), "FROM "+name+"\n", nil); err == nil {
			t.Fatal("reserved build reference accepted", name)
		}
	}
}

func TestBuildNetworkMustBelongToSession(t *testing.T) {
	p, _ := proxyFixture(t, "foreign-session")
	r := httptest.NewRequest("POST", "/build?networkmode="+strings.Repeat("c", 64), strings.NewReader("unused"))
	if err := p.build(r); err == nil {
		r.Body.Close()
		t.Fatal("build accepted foreign network")
	}
}

func TestForeignCopySourcesAndDeferredInstructionsAreRejected(t *testing.T) {
	p, engine := proxyFixture(t, exampleSession)
	id := "sha256:" + strings.Repeat("e", 64)
	engine.privateImages = map[string]string{id: "foreign-session"}
	for _, source := range []string{
		"\ufeffFROM " + id + "\n",
		"\ufeff\ufeffFROM " + id + "\n",
		"FROM public:1\nCOPY --from=" + id + " /private /stolen\n",
		"FROM public:1\nCOPY\t--from=" + id + " /private /stolen\n",
		"FRO\\\n\nM " + id + "\n",
		"FROM public:1\nONBUILD COPY --from=" + id + " /private /stolen\n",
		"FROM public:1\nADD http://foreign-service/private /private\n",
	} {
		if _, err := p.dockerfile(context.Background(), source, nil); err == nil {
			t.Fatal("foreign or deferred source bypassed instruction classification", source)
		}
	}
}

func TestBuildContextCannotReplaceValidatedDockerfileThroughArchiveLinks(t *testing.T) {
	for _, name := range []string{"../Dockerfile", "/Dockerfile", "linked-directory"} {
		t.Run(name, func(t *testing.T) {
			p, _ := proxyFixture(t, exampleSession)
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			source := "FROM public:1\n"
			writer.WriteHeader(&tar.Header{Name: "Dockerfile", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(source))})
			io.WriteString(writer, source)
			writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: "foreign"})
			writer.Close()
			if err := p.buildContext(context.Background(), &archive, io.Discard, "Dockerfile", nil); err == nil {
				t.Fatal("archive can change the validated Dockerfile path", name)
			}
		})
	}
}

func TestSharedBaseProvenanceRequiresGlobalReferenceOrExactMarker(t *testing.T) {
	id := "sha256:" + strings.Repeat("d", 64)
	for _, test := range []struct {
		name          string
		tags, digests []any
		shared        bool
	}{
		{name: "registry tag", tags: []any{"public:1"}, shared: true},
		{name: "registry digest", digests: []any{"example.invalid/base@" + id}, shared: true},
		{name: "exact host marker", tags: []any{"sdlc-test-base:" + strings.Repeat("d", 64)}, shared: true},
		{name: "qualified host marker", tags: []any{"docker.io/library/sdlc-test-base:" + strings.Repeat("d", 64)}, shared: true},
		{name: "forged qualified marker", tags: []any{"docker.io/library/sdlc-test-base:" + strings.Repeat("e", 64)}},
		{name: "ownerless intermediate"},
		{name: "reserved session alias", tags: []any{"docker.io/sdlc-test/foreign:tag"}},
		{name: "protected cache digest", digests: []any{"docker.io/library/sdlc-test-cache@" + id}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if sharedBaseImage(map[string]any{"Id": id, "RepoTags": test.tags, "RepoDigests": test.digests}) != test.shared {
				t.Fatal("image provenance misclassified")
			}
		})
	}
}

func TestBindSymlinkCannotLeaveOwnWorkspace(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	if err := os.Symlink(t.TempDir(), filepath.Join(p.config.LocalWorkspace, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.bindSource("/workspace/escape/file"); err == nil {
		t.Fatal("symlink escape accepted")
	}
	if _, err := p.bindSource("/workspace/../file"); err == nil {
		t.Fatal("traversal accepted")
	}
}

func TestListAndPruneFiltersKeepOtherSessionsOut(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	r := httptest.NewRequest("GET", "/containers/json?filters=%7B%22label%22%3A%5B%22example%3Dtrue%22%5D%7D", nil)
	if err := p.request(r, "/containers/json"); err != nil {
		t.Fatal(err)
	}
	var filters map[string][]string
	json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
	if len(filters["label"]) != 2 || filters["label"][1] != SessionLabel+"="+exampleSession {
		t.Fatal("namespace filter missing")
	}
}

func TestBuildContextRewritesOwnedImagesAndRetainsSource(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	alias := p.imageAlias("local-base:1")
	var contextBytes bytes.Buffer
	writer := tar.NewWriter(&contextBytes)
	files := map[string]string{"Dockerfile": "ARG BASE=local-base:1\nFROM ${BASE} as compile\nCOPY --from=compile /app /app\nFROM public:1\nCOPY --from=local-base:1 /data /data\n", "source.txt": "candidate source"}
	for name, data := range files {
		writer.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(data))})
		io.WriteString(writer, data)
	}
	writer.Close()
	var rewritten bytes.Buffer
	if err := p.buildContext(context.Background(), bytes.NewReader(contextBytes.Bytes()), &rewritten, "Dockerfile", nil); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(&rewritten)
	observed := map[string]string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(reader)
		observed[header.Name] = string(data)
	}
	if !strings.Contains(observed["Dockerfile"], "FROM "+alias+" as compile") || !strings.Contains(observed["Dockerfile"], "COPY --from="+alias) || !strings.Contains(observed["Dockerfile"], "COPY --from=compile") || observed["source.txt"] != "candidate source" {
		t.Fatal("build namespace or source changed incorrectly")
	}
}

func TestMissingImageKeepsDockerPullFallback(t *testing.T) {
	p, engine := proxyFixture(t, exampleSession)
	engine.missingImage = true
	for _, request := range []struct {
		method, path string
		body         any
	}{{"GET", "/images/public:1/json", nil}, {"POST", "/containers/create", map[string]any{"Image": "public:1"}}} {
		w := requestProxy(t, p, request.method, request.path, request.body)
		if w.Code != 404 {
			t.Fatalf("Docker cannot fall back to pull: %d %s", w.Code, w.Body.String())
		}
	}
	if source, err := p.dockerfile(context.Background(), "FROM public:1\n", nil); err != nil || source != "FROM public:1\n" {
		t.Fatal("missing named build base cannot use normal pull", source, err)
	}
}

func TestImageInspectHidesProtectedCacheAndUsesStableLogicalTag(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	alias := p.imageAlias("project-web")
	if p.imageAlias("project-web:latest") != alias {
		t.Fatal("default tags have different identities")
	}
	value := map[string]any{"RepoTags": []any{"sdlc-test-cache:immutable", alias}, "Config": map[string]any{"Image": alias}}
	for range 10 {
		data := p.rewriteResponse(value).(map[string]any)
		tags := array(data["RepoTags"])
		if len(tags) != 1 || tags[0] != "project-web:latest" || object(data["Config"])["Image"] != "project-web:latest" {
			t.Fatal("cache or nondeterministic tag exposed", data)
		}
	}
}

func TestLegacyDockerFiltersStillRestrictTheSession(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	r := httptest.NewRequest("GET", "/containers/json", nil)
	q := r.URL.Query()
	q.Set("filters", `{"label":{"project=true":true}}`)
	r.URL.RawQuery = q.Encode()
	if err := p.filters(r, true); err != nil {
		t.Fatal(err)
	}
	var filters map[string][]string
	json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
	if len(filters["label"]) != 2 || filters["label"][1] != SessionLabel+"="+exampleSession {
		t.Fatal("ownership filter absent", filters)
	}
}

func TestUnsupportedDockerfileSubstitutionDoesNotSelectWrongImage(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	for _, source := range []string{"ARG BASE=alpine\nFROM ${BASE:+debian}\n", "FROM ${BASE}\n", "# escape=`\nFROM public:1\n", "FRO\\\nM public:1\n", "COPY --fro\\\nm=public:1 /data /data\n"} {
		if _, err := p.dockerfile(context.Background(), source, map[string]string{"BASE": "alpine"}); err == nil {
			t.Fatal("unsupported expression silently changes image", source)
		}
	}
	if _, err := p.dockerfile(context.Background(), "FROM ${BASE}\n", map[string]string{"BASE": "public:1\nRUN echo injected"}); err == nil {
		t.Fatal("build argument injected Dockerfile instructions")
	}
}

func TestBuildScopesNetworkAndOverridesInheritedLabels(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	source := "FROM public:1 as build\nRUN true \\\n && true\nFROM scratch\nCOPY --from=0 /data /data\n"
	if err := w.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0600, Size: int64(len(source))}); err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, source)
	w.Close()
	r := httptest.NewRequest("POST", "/build", &archive)
	if err := p.build(r); err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		t.Fatal(err)
	}
	var labels map[string]string
	json.Unmarshal([]byte(r.URL.Query().Get("labels")), &labels)
	if r.URL.Query().Get("networkmode") != exampleSession+"-default" || labels[InternalLabel] != "false" || labels["io.sdlc.test-owner"] != "" || labels[SessionLabel] != exampleSession {
		t.Fatal("build can inherit controller ownership or use global networking", r.URL.Query())
	}
}

func TestNetworkInspectHidesOnlyTrustedProxyEndpoint(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	proxyID := exampleProxy
	serviceID := strings.Repeat("c", 64)
	network := map[string]any{"Name": exampleSession + "-fixed", "Containers": map[string]any{proxyID: map[string]string{"Name": "trusted-proxy"}, serviceID: map[string]string{"Name": exampleSession + "-service"}}}
	result := p.rewriteResponse(network).(map[string]any)
	containers := object(result["Containers"])
	if len(containers) != 1 || containers[serviceID] == nil {
		t.Fatal("network endpoints were not scoped transparently", containers)
	}
	if result["Name"] != "fixed" {
		t.Fatal("network name leaked namespace")
	}
}

func TestImageAnonymousVolumeCannotReuseCallerNamedVolume(t *testing.T) {
	p, engine := proxyFixture(t, exampleSession)
	if _, err := p.volume(context.Background(), "anonymous-1"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if w := requestProxy(t, p, "POST", "/containers/create", map[string]any{"Image": "public:1"}); w.Code != 201 {
			t.Fatal(w.Body.String())
		}
	}
	if len(engine.volumes) != 3 {
		t.Fatal("implicit volumes missing")
	}
	seen := map[string]bool{}
	for _, volume := range engine.volumes {
		name := stringValue(volume["Name"])
		if seen[name] {
			t.Fatal("anonymous volume silently reused named data", name)
		}
		seen[name] = true
	}
}

func TestHexadecimalResourceNamesRemainAccessible(t *testing.T) {
	p, _ := proxyFixture(t, exampleSession)
	name := strings.Repeat("e", 32)
	id := strings.Repeat("c", 64)
	transport := engineTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/containers/"+exampleSession+"-"+name+"/json" && r.URL.Path != "/containers/"+id+"/json" {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"not found"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Id": id, "Config": map[string]any{"Labels": map[string]string{SessionLabel: exampleSession}}})
	})}
	p.client.Transport, p.reverse.Transport = transport, transport
	if w := requestProxy(t, p, "GET", "/containers/"+name+"/json", nil); w.Code != 200 {
		t.Fatalf("scoped UUID name could not be inspected: %d %s", w.Code, w.Body.String())
	}
}
