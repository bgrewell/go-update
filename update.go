// Package update provides tooling to auto-update binary releases
// from GitHub based on the user's current version and operating system.
package update

import (
	"context"
	"fmt"
	"golang.org/x/oauth2"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/apex/log"
	"github.com/c4milo/unpackit"
	"github.com/mcuadros/go-version"
	"github.com/pkg/errors"
)

// Proxy is used to proxy a reader, for example
// using https://github.com/cheggaaa/pb to provide
// progress updates.
type Proxy func(int, io.ReadCloser) io.ReadCloser

// NopProxy does nothing.
var NopProxy = func(size int, r io.ReadCloser) io.ReadCloser {
	return r
}

// Manager is the update manager.
type Manager struct {
	Store          // Store for releases such as Github or a custom private store.
	Command string // Command is the executable's name.
}

// Release represents a project release.
type Release struct {
	Version     string    // Version is the release version.
	Notes       string    // Notes is the markdown release notes.
	URL         string    // URL is the notes url.
	PublishedAt time.Time // PublishedAt is the publish time.
	Assets      []*Asset  // Assets is the release assets.
}

// Asset represents a project release asset.
type Asset struct {
	Name      string // Name of the asset.
	Size      int    // Size of the asset.
	URL       string // URL of the asset.
	Downloads int    // Downloads count.
}

// InstallTo binary to the given dir.
func (m *Manager) InstallTo(path, dir string) error {
	log.Debugf("unpacking %q", path)

	f, err := os.Open(path)
	if err != nil {
		return errors.Wrap(err, "opening tarball")
	}

	tmpdir, err := unpackit.Unpack(f, "")
	if err != nil {
		f.Close()
		return errors.Wrap(err, "unpacking tarball")
	}

	if err := f.Close(); err != nil {
		return errors.Wrap(err, "closing tarball")
	}

	cmd := filepath.Base(m.Command)
	bin := filepath.Join(tmpdir, cmd)

	if err := os.Chmod(bin, 0755); err != nil {
		return errors.Wrap(err, "chmod")
	}

	dst := filepath.Join(dir, cmd)
	tmp := dst + ".tmp"

	log.Debugf("copy %q to %q", bin, tmp)
	if err := copyFile(tmp, bin); err != nil {
		return errors.Wrap(err, "copying")
	}

	if runtime.GOOS == "windows" {
		old := dst + ".old"
		log.Debugf("windows workaround renaming %q to %q", dst, old)
		if err := os.Rename(dst, old); err != nil {
			return errors.Wrap(err, "windows renaming")
		}
	}

	log.Debugf("renaming %q to %q", tmp, dst)
	if err := os.Rename(tmp, dst); err != nil {
		return errors.Wrap(err, "renaming")
	}

	return nil
}

// Install binary to replace the current version.
func (m *Manager) Install(path string) error {
	bin, err := exec.LookPath(m.Command)
	if err != nil {
		return errors.Wrapf(err, "looking up path of %q", m.Command)
	}

	dir := filepath.Dir(bin)
	return m.InstallTo(path, dir)
}

func (r *Release) Newer(currentVersion string) bool {
	return version.Compare(r.Version, currentVersion, ">")
}

// FindTarball returns a tarball matching os and arch, or nil.
func (r *Release) FindTarball(os, arch string) *Asset {
	s := strings.ToLower(fmt.Sprintf("%s_%s", os, arch))
	for _, a := range r.Assets {
		ext := filepath.Ext(a.Name)
		if strings.Contains(strings.ToLower(a.Name), s) && ext == ".gz" {
			return a
		}
	}

	return nil
}

// FindZip returns a zipfile matching os and arch, or nil.
func (r *Release) FindZip(os, arch string) *Asset {
	s := strings.ToLower(fmt.Sprintf("%s_%s", os, arch))
	for _, a := range r.Assets {
		ext := filepath.Ext(a.Name)
		if strings.Contains(strings.ToLower(a.Name), s) && ext == ".zip" {
			return a
		}
	}

	return nil
}

// Download the asset from a secure location to a tmp directory and return its path.
func (a *Asset) DownloadSecure(token string) (string, error) {
	return a.downloadProxySecure(NopProxy, &token)
}

// Download the asset to a tmp directory and return its path.
func (a *Asset) Download() (string, error) {
	return a.downloadProxySecure(NopProxy, nil)
}

// DownloadProxy the asset to a tmp directory and return its path.
func (a *Asset) DownloadProxy(proxy Proxy) (string, error) {
	return a.downloadProxySecure(proxy, nil)
}

// DownloadProxySecure the asset from a secure location to a tmp directory and return its path.
func (a *Asset) DownloadProxySecure(proxy Proxy, token string) (string, error) {
	return a.downloadProxySecure(proxy, &token)
}

func (a *Asset) downloadProxySecure(proxy Proxy, token *string) (string, error) {
	f, err := ioutil.TempFile(os.TempDir(), "update-")
	if err != nil {
		return "", errors.Wrap(err, "creating temp file")
	}

	log.Debugf("fetch %q", a.URL)

	var tc = &http.Client{}
	if token != nil {
		t := *token
		ctx := context.Background()
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{
				AccessToken: t,
			},
		)
		tc = oauth2.NewClient(ctx, ts)

	}

	req, _ := http.NewRequest("GET", a.URL, nil)
	req.Header.Set("Accept", "application/octet-stream")
	res, err := tc.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "fetching asset")
	}

	kind := res.Header.Get("Content-Type")
	size, _ := strconv.Atoi(res.Header.Get("Content-Length"))
	log.Debugf("response %s – %s (%d KiB)", res.Status, kind, size/1024)

	body := proxy(size, res.Body)

	if res.StatusCode >= 400 {
		body.Close()
		return "", errors.Wrap(err, res.Status)
	}

	log.Debugf("copy to %q", f.Name())
	if _, err := io.Copy(f, body); err != nil {
		body.Close()
		return "", errors.Wrap(err, "copying body")
	}

	if err := body.Close(); err != nil {
		return "", errors.Wrap(err, "closing body")
	}

	if err := f.Close(); err != nil {
		return "", errors.Wrap(err, "closing file")
	}

	log.Debugf("copied")
	return f.Name(), nil
}

// copyFile copies the contents of the file named src to the file named
// by dst. The file will be created if it does not already exist. If the
// destination file exists, all it's contents will be replaced by the contents
// of the source file. The file mode will be copied from the source and
// the copied data is synced/flushed to stable storage.
func copyFile(dst, src string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return
	}

	defer func() {
		if e := out.Close(); e != nil {
			err = e
		}
	}()

	_, err = io.Copy(out, in)
	if err != nil {
		return
	}

	err = out.Sync()
	if err != nil {
		return
	}

	si, err := os.Stat(src)
	if err != nil {
		return
	}

	err = os.Chmod(dst, si.Mode())
	if err != nil {
		return
	}

	return
}
