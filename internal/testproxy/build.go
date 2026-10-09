package testproxy

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var dockerImageToken = regexp.MustCompile(`(?i)^(FROM\s+(?:--platform=\S+\s+)?)(\S+)(.*)$`)
var copyImageToken = regexp.MustCompile(`(?i)(\s--from=)(\S+)`)
var buildArgument = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
var dockerEscapeDirective = regexp.MustCompile("(?i)^#\\s*escape\\s*=\\s*(\\S+)\\s*$")

func (p *Proxy) build(r *http.Request) error {
	query := r.URL.Query()
	if query.Get("version") == "2" || query.Get("remote") != "" || query.Get("networkmode") == "host" {
		return errors.New("shared checks require local-context Engine builds with DOCKER_BUILDKIT=0")
	}
	network, err := p.networkRef(r.Context(), query.Get("networkmode"))
	if err != nil {
		return err
	}
	query.Set("networkmode", network)
	if raw := query.Get("cachefrom"); raw != "" {
		var images []string
		if json.Unmarshal([]byte(raw), &images) != nil {
			return errors.New("invalid build cache references")
		}
		for i, image := range images {
			mapped, _, err := p.imageForSession(r.Context(), image)
			if err != nil {
				return err
			}
			images[i] = mapped
		}
		encoded, _ := json.Marshal(images)
		query.Set("cachefrom", string(encoded))
	}
	for _, tag := range query["t"] {
		alias := p.imageAlias(tag)
		query["t"] = replaceTag(query["t"], tag, alias)
	}
	labels := map[string]string{}
	if raw := query.Get("labels"); raw != "" && json.Unmarshal([]byte(raw), &labels) != nil {
		return errors.New("invalid build labels")
	}
	labels[SessionLabel] = p.config.Session
	labels[InternalLabel] = "false"
	labels["io.sdlc.test-owner"] = ""
	encoded, _ := json.Marshal(labels)
	query.Set("labels", string(encoded))
	arguments := map[string]string{}
	if raw := query.Get("buildargs"); raw != "" && json.Unmarshal([]byte(raw), &arguments) != nil {
		return errors.New("invalid build arguments")
	}
	filename := query.Get("dockerfile")
	if filename == "" {
		filename = "Dockerfile"
	}
	filename = path.Clean(filename)
	if path.IsAbs(filename) || filename == ".." || strings.HasPrefix(filename, "../") {
		return errors.New("invalid build Dockerfile path")
	}
	r.URL.RawQuery = query.Encode()
	input := r.Body
	reader, writer := io.Pipe()
	r.Body = reader
	r.ContentLength = -1
	r.Header.Del("Content-Length")
	r.Header.Del("Content-Encoding")
	go func() {
		defer input.Close()
		err := p.buildContext(r.Context(), input, writer, filename, arguments)
		writer.CloseWithError(err)
	}()
	return nil
}
func replaceTag(tags []string, old, next string) []string {
	for i, tag := range tags {
		if tag == old {
			tags[i] = next
		}
	}
	return tags
}

func (p *Proxy) buildContext(ctx context.Context, source io.Reader, destination io.Writer, filename string, arguments map[string]string) error {
	buffer := bufio.NewReader(source)
	var input io.Reader = buffer
	if magic, _ := buffer.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zipped, err := gzip.NewReader(buffer)
		if err != nil {
			return err
		}
		defer zipped.Close()
		input = zipped
	}
	reader, writer := tar.NewReader(input), tar.NewWriter(destination)
	found := false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if path.IsAbs(header.Name) {
			return errors.New("build context paths must be relative")
		}
		for _, component := range strings.Split(header.Name, "/") {
			if component == ".." {
				return errors.New("build context paths must not traverse parent directories")
			}
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
			return errors.New("shared build contexts support regular files and directories only")
		}
		if path.Clean(header.Name) == filename {
			if found || header.Size > 1<<20 || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) {
				return errors.New("build requires a bounded regular Dockerfile")
			}
			data, err := io.ReadAll(reader)
			if err != nil {
				return err
			}
			changed, err := p.dockerfile(ctx, string(data), arguments)
			if err != nil {
				return err
			}
			header.Size = int64(len(changed))
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
			if _, err := io.WriteString(writer, changed); err != nil {
				return err
			}
			found = true
		} else {
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
			if _, err := io.Copy(writer, reader); err != nil {
				return err
			}
		}
	}
	if !found {
		return errors.New("build context omitted its Dockerfile")
	}
	return writer.Close()
}

func (p *Proxy) dockerfile(ctx context.Context, source string, supplied map[string]string) (string, error) {
	source = strings.TrimLeft(source, "\ufeff")
	arguments := map[string]string{}
	global := true
	stages := map[string]bool{}
	stageCount := 0
	var result []string
	lines := strings.Split(source, "\n")
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) > 0 && (strings.EqualFold(fields[0], "ONBUILD") || strings.EqualFold(fields[0], "ADD")) {
			return "", errors.New("ADD and deferred Dockerfile instructions are unavailable in shared checks; use COPY")
		}
		if directive := dockerEscapeDirective.FindStringSubmatch(trimmed); directive != nil && directive[1] != "\\" {
			return "", errors.New("shared checks require the default Dockerfile escape character")
		}
		// Assemble continued instructions for classification, including split
		// instruction tokens. Preserve ordinary RUN formatting but reject all
		// multiline image instructions rather than forwarding unchecked inputs.
		start := index
		logical := line
		for strings.HasSuffix(strings.TrimRight(logical, " \t\r"), "\\") {
			logical = strings.TrimSuffix(strings.TrimRight(logical, " \t\r"), "\\")
			index++
			for index < len(lines) && (strings.TrimSpace(lines[index]) == "" || strings.HasPrefix(strings.TrimSpace(lines[index]), "#")) {
				index++
			}
			if index >= len(lines) {
				return "", errors.New("unterminated Dockerfile continuation")
			}
			logical += lines[index]
		}
		if index != start {
			fields := strings.Fields(logical)
			if len(fields) > 0 && (strings.EqualFold(fields[0], "FROM") || strings.EqualFold(fields[0], "COPY") || strings.EqualFold(fields[0], "ADD") || strings.EqualFold(fields[0], "ONBUILD")) {
				return "", errors.New("multiline image references require a disposable test daemon")
			}
			result = append(result, lines[start:index+1]...)
			continue
		}
		if global && len(fields) > 0 && strings.EqualFold(fields[0], "ARG") {
			parts := strings.SplitN(strings.TrimSpace(trimmed[3:]), "=", 2)
			if value, ok := supplied[parts[0]]; ok {
				arguments[parts[0]] = value
			} else if len(parts) == 2 {
				arguments[parts[0]] = parts[1]
			}
		}
		expand := func(value string) (string, error) {
			missing := false
			value = buildArgument.ReplaceAllStringFunc(value, func(token string) string {
				match := buildArgument.FindStringSubmatch(token)
				name := match[1]
				if name == "" {
					name = match[2]
				}
				val, exists := arguments[name]
				if !exists {
					missing = true
				}
				return val
			})
			if missing || !imageReferencePattern.MatchString(value) {
				return "", errors.New("build image argument is unresolved")
			}
			return value, nil
		}
		if match := dockerImageToken.FindStringSubmatch(trimmed); match != nil {
			global = false
			name, err := expand(match[2])
			if err != nil {
				return "", err
			}
			if !stages[strings.ToLower(name)] {
				name, err = p.buildImage(ctx, name)
				if err != nil {
					return "", err
				}
			}
			line = match[1] + name + match[3]
			stages[strconv.Itoa(stageCount)] = true
			stageCount++
			fields := strings.Fields(match[3])
			if len(fields) == 2 && strings.EqualFold(fields[0], "as") {
				stages[strings.ToLower(fields[1])] = true
			}
		} else if len(fields) > 0 && strings.EqualFold(fields[0], "COPY") {
			if strings.Contains(trimmed, "--from") && !copyImageToken.MatchString(trimmed) {
				return "", errors.New("unsupported Dockerfile image reference")
			}
			var rewriteErr error
			line = copyImageToken.ReplaceAllStringFunc(line, func(token string) string {
				match := copyImageToken.FindStringSubmatch(token)
				name, err := expand(match[2])
				if err != nil {
					rewriteErr = err
					return token
				}
				if !stages[strings.ToLower(name)] {
					name, err = p.buildImage(ctx, name)
					if err != nil {
						rewriteErr = err
						return token
					}
				}
				return match[1] + name
			})
			if rewriteErr != nil {
				return "", rewriteErr
			}
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n"), nil
}

func (p *Proxy) buildImage(ctx context.Context, name string) (string, error) {
	if name == "scratch" {
		return name, nil
	}
	mapped, metadata, err := p.imageForSession(ctx, name)
	if err == nil {
		if len(array(object(metadata["Config"])["OnBuild"])) != 0 {
			return "", errors.New("deferred base-image instructions are unavailable in shared checks")
		}
		return mapped, nil
	}
	var upstream *apiError
	if errors.As(err, &upstream) && upstream.code == http.StatusNotFound && !ReservedImageReference(name) && !strings.HasPrefix(name, "sha256:") && !objectID.MatchString(name) {
		// Missing named bases use the Engine's normal registry pull. Never
		// reinterpret a missing private ID or reserved alias as a public base.
		return name, nil
	}
	return "", err
}
