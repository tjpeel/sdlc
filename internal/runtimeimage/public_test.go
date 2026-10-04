package runtimeimage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type recordingPublicDocker struct {
	*fakeDocker
	publicCalls [][]string
}

func (docker *recordingPublicDocker) PublicRun(ctx context.Context, args ...string) error {
	docker.publicCalls = append(docker.publicCalls, append([]string{}, args...))
	return docker.fakeDocker.Run(ctx, args...)
}

func TestUpdatesUsePublicPullAndBuildMode(t *testing.T) {
	manager, fake, root, pins, _ := pinsFixture(t)
	docker := &recordingPublicDocker{fakeDocker: fake}
	manager.Docker = docker
	if _, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{Pins: &pins, Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if len(docker.publicCalls) != 3 || !reflect.DeepEqual(docker.publicCalls[0], []string{"pull", pins.SigningImage}) || !reflect.DeepEqual(docker.publicCalls[1], []string{"pull", pins.DaemonImage}) || docker.publicCalls[2][0] != "build" {
		t.Fatal("update did not isolate registry operations", docker.publicCalls)
	}
	for _, reference := range []string{"docker:latest", "docker:29.0.0-dind", "private.example/image@" + oldImage, "node:24.0.0-bookworm@" + oldImage} {
		if err := manager.PullPublic(context.Background(), reference); err == nil {
			t.Fatal("unsupported public pull accepted", reference)
		}
	}
	if len(docker.publicCalls) != 3 {
		t.Fatal("invalid reference reached Docker")
	}
}

func TestRefreshWithoutPinsUsesPublicBuildMode(t *testing.T) {
	manager, fake, root := fixture(t)
	docker := &recordingPublicDocker{fakeDocker: fake}
	manager.Docker = docker
	if _, err := manager.BuildWithOptions(context.Background(), root, BuildOptions{Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if len(docker.publicCalls) != 1 || docker.publicCalls[0][0] != "build" {
		t.Fatal("refresh build inherited ordinary registry credentials")
	}
}

func TestLocalPublicDockerHasPrivateConfigAndSelectedEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	for _, operation := range []string{"pull", "build", "failed pull"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			binaryDirectory := filepath.Join(root, "bin")
			pluginDirectory := binaryDirectory
			hostConfiguration := filepath.Join(root, "host-docker")
			for _, directory := range []string{binaryDirectory, pluginDirectory, hostConfiguration} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			plugin := filepath.Join(pluginDirectory, "docker-buildx")
			if err := os.WriteFile(plugin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hostConfiguration, "config.json"), []byte(`{"auths":{"private.example":{"auth":"disposable"}},"credsStore":"disposable"}`), 0600); err != nil {
				t.Fatal(err)
			}
			hostilePlugin := filepath.Join(hostConfiguration, "docker-buildx")
			if err := os.WriteFile(hostilePlugin, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
				t.Fatal(err)
			}
			metadata, _ := json.Marshal([]map[string]string{{"Name": "buildx", "Path": hostilePlugin}})
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
			pathRecord := filepath.Join(root, "config-path")
			argsRecord := filepath.Join(root, "args")
			environmentRecord := filepath.Join(root, "environment")
			configRecord := filepath.Join(root, "public-config.json")
			modeRecord := filepath.Join(root, "modes")
			infoRecord := filepath.Join(root, "host-plugin-discovery")
			endpoint := "unix:///tmp/disposable-selected-engine.sock"
			exitCode := "0"
			if operation == "failed pull" {
				exitCode = "7"
			}
			script := "#!/bin/sh\nset -eu\n" +
				"if [ \"$1\" = context ]; then printf '%s\\n' '\"" + endpoint + "\"'; exit 0; fi\n" +
				"if [ \"$1\" = info ]; then /usr/bin/touch " + quote(infoRecord) + "; printf '%s\\n' " + quote(string(metadata)) + "; exit 0; fi\n" +
				"[ \"$1\" = --config ]\n[ \"$2\" = \"$DOCKER_CONFIG\" ]\n[ \"$3\" = --host ]\n[ \"$4\" = " + quote(endpoint) + " ]\n" +
				"printf '%s\\n' \"$DOCKER_CONFIG\" > " + quote(pathRecord) + "\n" +
				"printf '%s\\n' \"$@\" > " + quote(argsRecord) + "\n" +
				"/usr/bin/env > " + quote(environmentRecord) + "\n" +
				"/bin/cat \"$DOCKER_CONFIG/config.json\" > " + quote(configRecord) + "\n" +
				"/bin/ls -ld \"$DOCKER_CONFIG\" \"$DOCKER_CONFIG/config.json\" > " + quote(modeRecord) + "\n" +
				"exit " + exitCode + "\n"
			if err := os.WriteFile(filepath.Join(binaryDirectory, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DOCKER_CONFIG", hostConfiguration)
			t.Setenv("DOCKER_CONTEXT", "disposable-selected-context")
			t.Setenv("DOCKER_HOST", "unix:///tmp/disposable-ignored-engine.sock")
			for _, key := range []string{"DOCKER_AUTH_CONFIG", "DOCKER_CUSTOM_HEADERS", "DOCKER_CREDENTIAL_HELPER", "BUILDX_CONFIG", "BUILDX_BUILDER", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CONFIG_DIR", "OP_SERVICE_ACCOUNT_TOKEN", "GH_TOKEN", "HTTP_PROXY"} {
				t.Setenv(key, "disposable-host-only-value")
			}
			args := []string{"pull", testPins().DaemonImage}
			if operation == "build" {
				args = []string{"build", "--pull", "--no-cache", root}
			}
			err := (LocalDocker{}).PublicRun(context.Background(), args...)
			if (operation == "failed pull") != (err != nil) {
				t.Fatal("public Docker operation returned an unexpected result", err)
			}
			if _, err := os.Stat(infoRecord); !os.IsNotExist(err) {
				t.Fatal("public operation consulted host-configured executable plugins")
			}
			data, readErr := os.ReadFile(pathRecord)
			if readErr != nil {
				t.Fatal(readErr)
			}
			private := strings.TrimSpace(string(data))
			if private == hostConfiguration || !strings.Contains(filepath.Base(private), "sdlc-public-docker-") {
				t.Fatal("public operation reused host Docker config")
			}
			if _, err := os.Stat(private); !os.IsNotExist(err) {
				t.Fatal("disposable Docker configuration was retained")
			}
			data, readErr = os.ReadFile(configRecord)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var configuration map[string]any
			if err := json.Unmarshal(data, &configuration); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), hostConfiguration) || strings.Contains(string(data), hostilePlugin) {
				t.Fatal("host-configured hostile plugin reached public operation")
			}
			auths, ok := configuration["auths"].(map[string]any)
			if !ok || len(auths) != 0 || configuration["credsStore"] != nil || configuration["credHelpers"] != nil {
				t.Fatal("public Docker configuration contains host registry authentication", configuration)
			}
			if operation == "build" {
				directories, ok := configuration["cliPluginsExtraDirs"].([]any)
				resolved, err := filepath.EvalSymlinks(pluginDirectory)
				if err != nil || !ok || len(directories) != 1 || directories[0] != resolved {
					t.Fatal("public build lost the native Buildx plugin")
				}
			} else if len(configuration) != 1 {
				t.Fatal("public pull retained unnecessary host configuration")
			}
			data, readErr = os.ReadFile(environmentRecord)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(data), "disposable-host-only-value") || strings.Contains(string(data), "DOCKER_CONTEXT=") || strings.Contains(string(data), "DOCKER_HOST=") {
				t.Fatal("host credentials or context override reached the public operation")
			}
			for _, expected := range []string{"DOCKER_CONFIG=" + private, "HOME=" + private, "USERPROFILE=" + private} {
				if !strings.Contains(string(data), expected) {
					t.Fatal("missing private environment", expected)
				}
			}
			data, readErr = os.ReadFile(modeRecord)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(data), "drwx------") || !strings.Contains(string(data), "-rw-------") {
				t.Fatal("public Docker configuration was not private")
			}
		})
	}
}

func TestPublicDockerRejectsRemoteEndpointBeforeStartingOperation(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "")
	for _, endpoint := range []string{"tcp://example.invalid:2376", "ssh://example.invalid", "unix:///tmp/disposable.sock\nmalformed"} {
		t.Setenv("DOCKER_HOST", endpoint)
		if err := (LocalDocker{}).PublicRun(context.Background(), "pull", testPins().DaemonImage); err == nil {
			t.Fatal("unsupported endpoint accepted")
		}
	}
}
