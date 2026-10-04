package runtimepins

import (
	"bytes"
	"strings"
	"testing"
)

func fixturePins() Pins {
	pins := Pins{Arguments: map[string]string{}, Images: map[string]string{}, SigningImage: "1password/op:2.40.0@sha256:" + strings.Repeat("d", 64), DaemonImage: "docker:30.0.0-dind@sha256:" + strings.Repeat("e", 64)}
	for _, name := range argumentNames {
		pins.Arguments[name] = "1.2.3"
	}
	pins.Arguments["SKILLS_REVISION"], pins.Arguments["AGENTS_REVISION"] = strings.Repeat("a", 40), strings.Repeat("b", 40)
	pins.Images["node"] = "node:24.2.0-bookworm@sha256:" + strings.Repeat("a", 64)
	pins.Images["golang"] = "golang:1.27.2-bookworm@sha256:" + strings.Repeat("b", 64)
	pins.Images["docker"] = "docker:30.0.0-cli@sha256:" + strings.Repeat("c", 64)
	return pins
}

func fixtureRecipe() []byte {
	lines := []string{
		"FROM golang:1.27.1-bookworm@sha256:" + strings.Repeat("a", 64) + " AS publisher-build",
		"RUN go build ./cmd/example",
		"FROM docker:29.8.2-cli@sha256:" + strings.Repeat("b", 64) + " AS docker-tools",
		"FROM node:24-bookworm@sha256:" + strings.Repeat("c", 64),
	}
	for _, name := range argumentNames {
		value := "0.1.0"
		if name == "SKILLS_REVISION" || name == "AGENTS_REVISION" {
			value = strings.Repeat("c", 40)
		}
		if name == "NPM_VERSION" || name == "YARN_VERSION" || name == "COMPOSE_VERSION" || name == "BUILDX_VERSION" {
			value = ""
		}
		lines = append(lines, "ARG "+name+"="+value)
	}
	return []byte(strings.Join(lines, "\n") + "\nRUN echo kept\n")
}

func TestApplyOnlyPrivateRecipeAndPreserveOtherInstructions(t *testing.T) {
	original := fixtureRecipe()
	before := append([]byte{}, original...)
	pins := fixturePins()
	result, err := ApplyDockerfile(original, pins)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, before) {
		t.Fatal("source recipe changed")
	}
	if !bytes.Contains(result, []byte("RUN echo kept\n")) || !bytes.Contains(result, []byte("RUN go build ./cmd/example")) {
		t.Fatal("unmanaged instructions changed")
	}
	for name, value := range pins.Arguments {
		if !bytes.Contains(result, []byte("ARG "+name+"="+value+"\n")) {
			t.Fatal("argument not pinned", name)
		}
	}
	read, err := ReadDockerfile(result)
	if err != nil || read.Images["node"] != pins.Images["node"] || read.Arguments["NPM_VERSION"] != pins.Arguments["NPM_VERSION"] {
		t.Fatal("cannot read patched recipe", err)
	}
}

func TestReadLegacyRecipeButRequireUpdateArgumentsWhenApplying(t *testing.T) {
	recipe := string(fixtureRecipe())
	for _, name := range []string{"NPM_VERSION", "YARN_VERSION", "COMPOSE_VERSION", "BUILDX_VERSION"} {
		recipe = strings.ReplaceAll(recipe, "ARG "+name+"=\n", "")
	}
	read, err := ReadDockerfile([]byte(recipe))
	if err != nil || read.Images["docker"] == "" || read.Arguments["CODEX_VERSION"] == "" {
		t.Fatal("legacy image provenance could not be read", err)
	}
	for _, name := range []string{"NPM_VERSION", "YARN_VERSION", "COMPOSE_VERSION", "BUILDX_VERSION"} {
		if value, exists := read.Arguments[name]; !exists || value != "" {
			t.Fatal("legacy bundled dependency was invented", name, value)
		}
	}
	if _, err := ApplyDockerfile([]byte(recipe), fixturePins()); err == nil {
		t.Fatal("update applied to a source lacking installation instructions")
	}
}

func TestRejectUnsafePinsAndIncompleteMaps(t *testing.T) {
	for _, mutate := range []func(*Pins){
		func(p *Pins) { p.Arguments["CODEX_VERSION"] = "1.2.3\nRUN injected" },
		func(p *Pins) { p.Arguments["CODEX_VERSION"] = "1.2.3-beta" },
		func(p *Pins) { p.Arguments["EXTRA"] = "1.2.3" },
		func(p *Pins) { delete(p.Arguments, "YARN_VERSION") },
		func(p *Pins) { p.Images["node"] = "node:24-bookworm@sha256:" + strings.Repeat("a", 64) },
		func(p *Pins) {
			p.Images["node"] = "mirror.invalid/node:24.1.0-bookworm@sha256:" + strings.Repeat("a", 64)
		},
		func(p *Pins) { p.Images["golang"] = "golang:1.27.2-alpine@sha256:" + strings.Repeat("a", 64) },
		func(p *Pins) { p.DaemonImage = "docker:30.0.0-dind" },
		func(p *Pins) { p.SigningImage = "1password/op:3.0.0@sha256:" + strings.Repeat("a", 64) },
	} {
		pins := fixturePins()
		mutate(&pins)
		if _, err := ApplyDockerfile(fixtureRecipe(), pins); err == nil {
			t.Fatal("unsafe pins accepted", pins)
		}
	}
}

func TestRecipeRejectsMissingDuplicateOrAlternativeDeclarations(t *testing.T) {
	original := string(fixtureRecipe())
	for _, recipe := range []string{
		original + "ARG CODEX_VERSION=2.3.4\n",
		strings.Replace(original, "ARG CODEX_VERSION=0.1.0\n", "", 1),
		original + " arg CODEX_VERSION=2.3.4\n",
		original + "FROM alpine:latest\n",
		strings.Replace(original, "AS docker-tools", "AS other-tools", 1),
		strings.Replace(original, "ARG NPM_VERSION=", "ARG NPM_VERSION", 1),
		strings.Replace(original, "ARG GH_VERSION=0.1.0", "ARG GH_VERSION=0.1.0 # comment", 1),
	} {
		if _, err := ApplyDockerfile([]byte(recipe), fixturePins()); err == nil {
			t.Fatal("ambiguous recipe accepted")
		}
	}
}
