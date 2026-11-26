package httpapi

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"regexp"
	"strings"
)

// This is a build-pairing observation, not release ordering, authentication,
// an atomic deployment check, or proof that the worker's cached assets exist.
type webBuildInfo struct {
	worker  string
	version string
}

var (
	errInvalidWebBuild = errors.New("web build metadata is unavailable or inconsistent")
	webBuildPattern    = regexp.MustCompile(`^\{"worker":"(/pwa-worker-[a-f0-9]{24}\.js)","web_version":"([A-Za-z0-9]+(?:[.+-][A-Za-z0-9]+)*)"\}\n\z`)
)

func readWebBuild(assets fs.FS) (webBuildInfo, error) {
	if assets == nil {
		return webBuildInfo{}, errInvalidWebBuild
	}
	build, err := readBuildMetadata(assets, "web-build.json")
	if err != nil {
		return webBuildInfo{}, errInvalidWebBuild
	}
	match := webBuildPattern.FindSubmatch(build)
	if len(match) != 3 || len(match[2]) > 32 {
		return webBuildInfo{}, errInvalidWebBuild
	}
	worker, version := string(match[1]), string(match[2])
	descriptor, err := readBuildMetadata(assets, "pwa-version.json")
	if err != nil || !bytes.Equal(descriptor, []byte(`{"worker":"`+worker+`"}`+"\n")) {
		return webBuildInfo{}, errInvalidWebBuild
	}
	info, err := fs.Stat(assets, strings.TrimPrefix(worker, "/"))
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return webBuildInfo{}, errInvalidWebBuild
	}
	return webBuildInfo{worker: worker, version: version}, nil
}

func readBuildMetadata(assets fs.FS, name string) ([]byte, error) {
	file, err := assets.Open(name)
	if err != nil {
		return nil, errInvalidWebBuild
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 256 {
		return nil, errInvalidWebBuild
	}
	value, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || int64(len(value)) != info.Size() {
		return nil, errInvalidWebBuild
	}
	return value, nil
}
