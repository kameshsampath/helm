/*
Copyright 2016 The Kubernetes Authors All rights reserved.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package downloader

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/helm/cmd/helm/helmpath"
	"k8s.io/helm/pkg/provenance"
	"k8s.io/helm/pkg/repo"
)

// VerificationStrategy describes a strategy for determining whether to verify a chart.
type VerificationStrategy int

const (
	// VerifyNever will skip all verification of a chart.
	VerifyNever VerificationStrategy = iota
	// VerifyIfPossible will attempt a verification, it will not error if verification
	// data is missing. But it will not stop processing if verification fails.
	VerifyIfPossible
	// VerifyAlways will always attempt a verification, and will fail if the
	// verification fails.
	VerifyAlways
)

// ChartDownloader handles downloading a chart.
//
// It is capable of performing verifications on charts as well.
type ChartDownloader struct {
	// Out is the location to write warning and info messages.
	Out io.Writer
	// Verify indicates what verification strategy to use.
	Verify VerificationStrategy
	// Keyring is the keyring file used for verification.
	Keyring string
	// HelmHome is the $HELM_HOME.
	HelmHome helmpath.Home
}

// DownloadTo retrieves a chart. Depending on the settings, it may also download a provenance file.
//
// If Verify is set to VerifyNever, the verification will be nil.
// If Verify is set to VerifyIfPossible, this will return a verification (or nil on failure), and print a warning on failure.
// If Verify is set to VerifyAlways, this will return a verification or an error if the verification fails.
//
// For VerifyNever and VerifyIfPossible, the Verification may be empty.
func (c *ChartDownloader) DownloadTo(ref string, dest string) (*provenance.Verification, error) {
	// resolve URL
	u, err := c.ResolveChartRef(ref)
	if err != nil {
		return nil, err
	}
	data, err := download(u.String())
	if err != nil {
		return nil, err
	}

	name := filepath.Base(u.Path)
	destfile := filepath.Join(dest, name)
	if err := ioutil.WriteFile(destfile, data.Bytes(), 0655); err != nil {
		return nil, err
	}

	// If provenance is requested, verify it.
	ver := &provenance.Verification{}
	if c.Verify > VerifyNever {

		body, err := download(u.String() + ".prov")
		if err != nil {
			if c.Verify == VerifyAlways {
				return ver, fmt.Errorf("Failed to fetch provenance %q", u.String()+".prov")
			}
			fmt.Fprintf(c.Out, "WARNING: Verification not found for %s: %s\n", ref, err)
			return ver, nil
		}
		provfile := destfile + ".prov"
		if err := ioutil.WriteFile(provfile, body.Bytes(), 0655); err != nil {
			return nil, err
		}

		ver, err = VerifyChart(destfile, c.Keyring)
		if err != nil {
			// Fail always in this case, since it means the verification step
			// failed.
			return ver, err
		}
	}
	return ver, nil
}

// ResolveChartRef resolves a chart reference to a URL.
//
// A reference may be an HTTP URL, a 'reponame/chartname' reference, or a local path.
func (c *ChartDownloader) ResolveChartRef(ref string) (*url.URL, error) {
	// See if it's already a full URL.
	u, err := url.ParseRequestURI(ref)
	if err == nil {
		// If it has a scheme and host and path, it's a full URL
		if u.IsAbs() && len(u.Host) > 0 && len(u.Path) > 0 {
			return u, nil
		}
		return u, fmt.Errorf("Invalid chart url format: %s", ref)
	}

	r, err := repo.LoadRepositoriesFile(c.HelmHome.RepositoryFile())
	if err != nil {
		return u, err
	}

	// See if it's of the form: repo/path_to_chart
	p := strings.Split(ref, "/")
	if len(p) > 1 {
		if baseURL, ok := r.Repositories[p[0]]; ok {
			if !strings.HasSuffix(baseURL, "/") {
				baseURL = baseURL + "/"
			}
			return url.ParseRequestURI(baseURL + strings.Join(p[1:], "/"))
		}
		return u, fmt.Errorf("No such repo: %s", p[0])
	}
	return u, fmt.Errorf("Invalid chart url format: %s", ref)
}

// VerifyChart takes a path to a chart archive and a keyring, and verifies the chart.
//
// It assumes that a chart archive file is accompanied by a provenance file whose
// name is the archive file name plus the ".prov" extension.
func VerifyChart(path string, keyring string) (*provenance.Verification, error) {
	// For now, error out if it's not a tar file.
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if fi.IsDir() {
		return nil, errors.New("unpacked charts cannot be verified")
	} else if !isTar(path) {
		return nil, errors.New("chart must be a tgz file")
	}

	provfile := path + ".prov"
	if _, err := os.Stat(provfile); err != nil {
		return nil, fmt.Errorf("could not load provenance file %s: %s", provfile, err)
	}

	sig, err := provenance.NewFromKeyring(keyring, "")
	if err != nil {
		return nil, fmt.Errorf("failed to load keyring: %s", err)
	}
	return sig.Verify(path, provfile)
}

// download performs a simple HTTP Get and returns the body.
func download(href string) (*bytes.Buffer, error) {
	buf := bytes.NewBuffer(nil)

	resp, err := http.Get(href)
	if err != nil {
		return buf, err
	}
	if resp.StatusCode != 200 {
		return buf, fmt.Errorf("Failed to fetch %s : %s", href, resp.Status)
	}

	_, err = io.Copy(buf, resp.Body)
	resp.Body.Close()
	return buf, err
}

// isTar tests whether the given file is a tar file.
//
// Currently, this simply checks extension, since a subsequent function will
// untar the file and validate its binary format.
func isTar(filename string) bool {
	return strings.ToLower(filepath.Ext(filename)) == ".tgz"
}
