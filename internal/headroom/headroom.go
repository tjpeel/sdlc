// Package headroom manages a private, invocation-scoped compression proxy.
package headroom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const Image = "sdlc-headroom:0.39.1"
const Version = "0.39.1"
const PolicyVersion = "lossless-v1"

type Config struct {
	Mode          string `json:"mode,omitempty"`
	ImageID       string `json:"image_id,omitempty"`
	PolicyVersion string `json:"policy_version,omitempty"`
}

func (c Config) Enabled() bool { return c.Mode != "" && c.Mode != "off" }

var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (c Config) Validate() error {
	if !c.Enabled() {
		if c.ImageID != "" || c.PolicyVersion != "" {
			return fmt.Errorf("direct provider mode cannot carry Headroom settings")
		}
		return nil
	}
	if c.Mode != "passthrough" && c.Mode != "optimize" {
		return fmt.Errorf("Headroom mode must be off, passthrough or optimize")
	}
	if !imageID.MatchString(c.ImageID) || c.PolicyVersion != PolicyVersion {
		return fmt.Errorf("Headroom requires a resolved local image and the supported policy")
	}
	return nil
}

type Docker interface {
	Output(context.Context, ...string) ([]byte, error)
}

func Selection(mode string) (Config, error) {
	if mode == "" || mode == "off" {
		return Config{}, nil
	}
	if mode != "passthrough" && mode != "optimize" {
		return Config{}, fmt.Errorf("Headroom mode must be off, passthrough or optimize")
	}
	return Config{Mode: mode, PolicyVersion: PolicyVersion}, nil
}
func Resolve(ctx context.Context, docker Docker, mode string) (Config, error) {
	c, selectionErr := Selection(mode)
	if selectionErr != nil {
		return c, selectionErr
	}
	if !c.Enabled() {
		return c, c.Validate()
	}
	if mode != "passthrough" && mode != "optimize" {
		return c, fmt.Errorf("Headroom mode must be off, passthrough or optimize")
	}
	output, err := docker.Output(ctx, "image", "inspect", Image, "--format", inspectFormat)
	if err != nil {
		return c, fmt.Errorf("Headroom image is unavailable; build %s first", Image)
	}
	id, inspectErr := verifiedImage(output)
	if inspectErr != nil {
		return c, inspectErr
	}
	c.ImageID = id
	c.PolicyVersion = PolicyVersion
	return c, c.Validate()
}

// Stats contains only bounded numeric metadata, never request bodies or log text.
type Stats struct {
	Version      string `json:"version"`
	Mode         string `json:"mode"`
	Requests     *int64 `json:"requests"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	SavedTokens  *int64 `json:"saved_tokens"`
	Source       string `json:"source"`
	Complete     bool   `json:"complete"`
	Error        string `json:"error,omitempty"`
}
type Instance struct {
	docker Docker
	Name   string
	config Config
}

func Args(c Config, name string) []string {
	args := []string{"run", "--detach", "--rm", "--name", name, "--pull", "never", "--log-driver", "none", "--network", "bridge", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "2g", "--cpus", "2", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777"}
	for _, value := range []string{"HOME=/tmp", "HEADROOM_WORKSPACE_DIR=/tmp/headroom", "HEADROOM_CONFIG_DIR=/tmp/headroom/config", "HEADROOM_BEACON=off", "HEADROOM_UPDATE_CHECK=off", "LITELLM_LOCAL_MODEL_COST_MAP=True", "HEADROOM_STATELESS=1", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "DO_NOT_TRACK=1", "HEADROOM_TELEMETRY=on", "HEADROOM_SAVINGS_PROFILE=coding", "HEADROOM_COMPRESS_USER_MESSAGES=0", "HEADROOM_TOOL_SEARCH=0", "HEADROOM_DEDUPE=0", "HEADROOM_LOSSLESS_THEN_LOSSY=0", "HEADROOM_OUTPUT_SHAPER=0", "HEADROOM_MODEL_ROUTER_ENABLED=0", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "http_proxy=", "https_proxy=", "all_proxy="} {
		args = append(args, "--env", value)
	}
	args = append(args, c.ImageID, "--host", "127.0.0.1", "--port", "8787", "--mode", "cache", "--lossless", "--no-ccr", "--disable-kompress", "--disable-kompress-fallback", "--no-cache", "--no-rate-limit", "--retry-max-attempts", "1", "--no-code-aware", "--no-read-lifecycle", "--stateless", "--telemetry")
	if c.Mode == "passthrough" {
		args = append(args, "--no-optimize")
	}
	return args
}

const readyScript = `import urllib.request; urllib.request.urlopen("http://127.0.0.1:8787/readyz", timeout=2).read()`
const statsScript = `import json,urllib.request; d=json.load(urllib.request.urlopen("http://127.0.0.1:8787/stats",timeout=3)); print(json.dumps({"requests":d.get("requests",{}).get("total"),"input_tokens":d.get("tokens",{}).get("input"),"output_tokens":d.get("tokens",{}).get("output"),"saved_tokens":d.get("tokens",{}).get("saved")}))`

func Start(ctx context.Context, docker Docker, c Config, name string) (*Instance, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Enabled() {
		return nil, nil
	}
	// Recorded identity must still exist; never resolve the tag again on resume.
	output, err := docker.Output(ctx, "image", "inspect", c.ImageID, "--format", inspectFormat)
	id, inspectErr := verifiedImage(output)
	if err != nil || inspectErr != nil || id != c.ImageID {
		return nil, fmt.Errorf("recorded Headroom image is unavailable")
	}
	p := &Instance{docker: docker, Name: name, config: c}
	if _, err = docker.Output(ctx, Args(c, name)...); err != nil {
		cleanupErr := p.Close()
		if cleanupErr != nil {
			return nil, cleanupErr
		}
		return nil, fmt.Errorf("Headroom proxy could not start")
	}
	ready, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		if _, err = docker.Output(ready, "exec", name, "python", "-c", readyScript); err == nil {
			return p, nil
		}
		select {
		case <-ready.Done():
			cleanupErr := p.Close()
			if cleanupErr != nil {
				return nil, cleanupErr
			}
			return nil, fmt.Errorf("Headroom proxy did not become ready")
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (p *Instance) Stats(ctx context.Context) Stats {
	s := Stats{Version: Version, Mode: p.config.Mode, Source: "headroom_proxy_aggregate"}
	b, err := p.docker.Output(ctx, "exec", p.Name, "python", "-c", statsScript)
	if err != nil || len(b) > 4096 || json.Unmarshal(b, &s) != nil || negative(s.Requests) || negative(s.InputTokens) || negative(s.OutputTokens) || negative(s.SavedTokens) {
		return Stats{Version: Version, Mode: p.config.Mode, Source: "headroom_proxy_aggregate", Error: "proxy metrics unavailable"}
	}
	s.Complete = s.Requests != nil && s.InputTokens != nil && s.OutputTokens != nil && s.SavedTokens != nil
	return s
}
func negative(n *int64) bool { return n != nil && *n < 0 }
func (p *Instance) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := p.docker.Output(ctx, "ps", "--all", "--filter", "name=^/"+p.Name+"$", "--format", "{{.ID}}")
	if err == nil && strings.TrimSpace(string(b)) != "" {
		_, err = p.docker.Output(ctx, "rm", "--force", p.Name)
	}
	if err != nil {
		return fmt.Errorf("Headroom cleanup failed; inspect Docker before another run")
	}
	return nil
}

const inspectFormat = `{{json .}}`

func verifiedImage(output []byte) (string, error) {
	var info struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if len(output) > 1024*1024 || json.Unmarshal(output, &info) != nil || !imageID.MatchString(info.ID) || info.Config.Labels["io.sdlc.headroom.version"] != Version || info.Config.Labels["io.sdlc.headroom.policy"] != PolicyVersion {
		return "", fmt.Errorf("Headroom image version or policy does not match; rebuild the pinned image")
	}
	return info.ID, nil
}

// InputDocker accepts the fixed Dockerfile over stdin, without a host build context.
type InputDocker interface {
	Input(context.Context, io.Reader, ...string) ([]byte, error)
}

const Dockerfile = `FROM python:3.12-slim
ENV TIKTOKEN_CACHE_DIR=/opt/tiktoken
RUN pip install --no-cache-dir 'headroom-ai[proxy]==0.39.1' \
    && python -c 'import tiktoken; tiktoken.get_encoding("cl100k_base"); tiktoken.get_encoding("o200k_base")'
LABEL io.sdlc.headroom.version="0.39.1" io.sdlc.headroom.policy="lossless-v1"
USER 1000:1000
ENV HOME=/tmp
ENTRYPOINT ["headroom", "proxy"]
`

func Build(ctx context.Context, docker Docker) (Config, error) {
	input, ok := docker.(InputDocker)
	if !ok {
		return Config{}, fmt.Errorf("Docker adapter cannot build the pinned Headroom image")
	}
	if _, err := input.Input(ctx, bytes.NewBufferString(Dockerfile), "build", "--tag", Image, "-"); err != nil {
		return Config{}, fmt.Errorf("Headroom image build failed")
	}
	return Resolve(ctx, docker, "optimize")
}
