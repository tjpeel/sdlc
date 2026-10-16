package headroom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ValidateProxyURL accepts a proxy origin, never credentials or a dashboard path.
func ValidateProxyURL(address string) (string, error) {
	u, err := url.Parse(address)
	if len(address) > 2048 || err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.ContainsAny(address, "\r\n\t ") {
		return "", fmt.Errorf("Headroom proxy URL must be an http(s) origin without credentials, path, query or fragment (omit /dashboard and /v1)")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("Headroom proxy URL has an invalid port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("Headroom proxy URL has an invalid port")
	}
	u.Path = ""
	return u.String(), nil
}

// WorkerURL translates host loopback for local Docker workers. The saved address
// remains the user's host address; it never refers to a container's loopback.
func (c Config) WorkerURL() string {
	u, _ := url.Parse(c.ProxyURL)
	if u == nil {
		return ""
	}
	ip := net.ParseIP(u.Hostname())
	if strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback()) {
		host := "host.docker.internal"
		if u.Port() != "" {
			host = net.JoinHostPort(host, u.Port())
		}
		u.Host = host
	}
	return strings.TrimRight(u.String(), "/")
}

func Load(directory string) (Config, error) {
	path := filepath.Join(directory, "headroom.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Config{}, fmt.Errorf("Headroom proxy address is required; set it with sdlc runtime headroom set --proxy-url URL")
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return Config{}, fmt.Errorf("Headroom settings must be a regular JSON file at most 4 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("cannot open Headroom settings")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Config{}, fmt.Errorf("Headroom settings changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return Config{}, fmt.Errorf("cannot read bounded Headroom settings")
	}
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF || c.Mode != "external" {
		return Config{}, fmt.Errorf("Headroom settings require an external proxy URL")
	}
	return c, c.Validate()
}

func Save(directory, address string) (Config, error) {
	address, err := ValidateProxyURL(address)
	if err != nil {
		return Config{}, err
	}
	c := Config{Mode: "external", ProxyURL: address}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return Config{}, err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return Config{}, err
	}
	f, err := os.CreateTemp(directory, ".headroom-*")
	if err != nil {
		return Config{}, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return Config{}, err
	}
	if err = f.Close(); err != nil {
		return Config{}, err
	}
	if err = os.Rename(f.Name(), filepath.Join(directory, "headroom.json")); err != nil {
		return Config{}, err
	}
	return c, nil
}
