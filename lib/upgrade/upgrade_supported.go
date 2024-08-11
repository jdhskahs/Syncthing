// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !noupgrade && !ios
// +build !noupgrade,!ios

package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/syncthing/syncthing/lib/dialer"
	"github.com/syncthing/syncthing/lib/signature"
	"golang.org/x/net/http2"
)

const DisabledByCompilation = false

const (
	// Current binary size hovers around 10 MB. We give it some room to grow
	// and say that we never expect the binary to be larger than 64 MB.
	maxBinarySize = 64 << 20 // 64 MiB

	// The max expected size of the signature file.
	maxSignatureSize = 10 << 10 // 10 KiB

	// The max expected size of the compatibility.json file.
	maxCompatibilitySize = 1 << 10 // 1 KiB

	// We set the same limit on the archive. The binary will compress and we
	// include some other stuff - currently the release archive size is
	// around 6 MB.
	maxArchiveSize = maxBinarySize

	// When looking through the archive for the binary and signature, stop
	// looking once we've searched this many files.
	maxArchiveMembers = 100

	// Archive reads, or metadata checks, that take longer than this will be
	// rejected.
	readTimeout = 30 * time.Minute

	// The limit on the size of metadata that we accept.
	maxMetadataSize = 10 << 20 // 10 MiB

	unsupportedKernel = "The upgrade was compiled with %v which requires OS " +
		"version %s or later, but this system is currently running version %s"
)

// This is an HTTP/HTTPS client that does *not* perform certificate
// validation. We do this because some systems where Syncthing runs have
// issues with old or missing CA roots. It doesn't actually matter that we
// load the upgrade insecurely as we verify an ECDSA signature of the actual
// binary contents before accepting the upgrade.
var insecureHTTP = &http.Client{
	Timeout: readTimeout,
	Transport: &http.Transport{
		DialContext: dialer.DialContext,
		Proxy:       http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	},
}

func init() {
	_ = http2.ConfigureTransport(insecureHTTP.Transport.(*http.Transport))
}

func insecureGet(url, version string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", fmt.Sprintf(`syncthing %s (%s %s-%s)`, version, runtime.Version(), runtime.GOOS, runtime.GOARCH))
	return insecureHTTP.Do(req)
}

// FetchLatestReleases returns the latest releases. The "current" parameter
// is used for setting the User-Agent only.
func FetchLatestReleases(releasesURL, current string) []Release {
	resp, err := insecureGet(releasesURL, current)
	if err != nil {
		l.Infoln("Couldn't fetch release information:", err)
		return nil
	}
	if resp.StatusCode > 299 {
		l.Infoln("API call returned HTTP error:", resp.Status)
		return nil
	}

	var rels []Release
	err = json.NewDecoder(io.LimitReader(resp.Body, maxMetadataSize)).Decode(&rels)
	if err != nil {
		l.Infoln("Fetching release information:", err)
	}
	resp.Body.Close()

	return rels
}

type SortByRelease []Release

func (s SortByRelease) Len() int {
	return len(s)
}
func (s SortByRelease) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s SortByRelease) Less(i, j int) bool {
	return CompareVersions(s[i].Tag, s[j].Tag) > 0
}

func LatestRelease(releasesURL, current string, upgradeToPreReleases bool) (Release, error) {
	rels := FetchLatestReleases(releasesURL, current)
	rel, err := SelectLatestRelease(rels, current, upgradeToPreReleases)
	if err != nil {
		return rel, err
	}

	return rel, nil
}

func SelectLatestRelease(rels []Release, current string, upgradeToPreReleases bool) (Release, error) {
	if len(rels) == 0 {
		return Release{}, ErrNoVersionToSelect
	}

	// Sort the releases, lowest version number first
	sort.Sort(sort.Reverse(SortByRelease(rels)))

	var selected Release
	for _, rel := range rels {
		if CompareVersions(rel.Tag, current) == MajorNewer {
			// We've found a new major version. That's fine, but if we've
			// already found a minor upgrade that is acceptable we should go
			// with that one first and then revisit in the future.
			if selected.Tag != "" && CompareVersions(selected.Tag, current) == Newer {
				return selected, nil
			}
		}

		if rel.Prerelease && !upgradeToPreReleases {
			l.Debugln("skipping pre-release", rel.Tag)
			continue
		}

		expectedReleases := releaseNames(rel.Tag)
	nextAsset:
		for _, asset := range rel.Assets {
			assetName := path.Base(asset.Name)
			// Check for the architecture
			for _, expRel := range expectedReleases {
				if strings.HasPrefix(assetName, expRel) {
					l.Debugln("selected", rel.Tag)
					selected = rel
					break nextAsset
				}
			}
		}
	}

	if selected.Tag == "" {
		return Release{}, ErrNoReleaseDownload
	}

	return selected, nil
}

// Upgrade to the given release, saving the previous binary with a ".old" extension.
func upgradeTo(binary string, rel Release) error {
	expectedReleases := releaseNames(rel.Tag)
	for _, asset := range rel.Assets {
		assetName := path.Base(asset.Name)
		l.Debugln("considering release", assetName)

		for _, expRel := range expectedReleases {
			if strings.HasPrefix(assetName, expRel) {
				rt, err := upgradeToURL(assetName, binary, asset.URL)
				if err != nil {
					return err
				}
				return verifyCompatibility(rel, rt)
			}
		}
	}

	return ErrNoReleaseDownload
}

// Upgrade to the given release, saving the previous binary with a ".old" extension.
func upgradeToURL(archiveName, binary string, url string) (string, error) {
	fname, rt, err := readRelease(archiveName, filepath.Dir(binary), url)
	if err != nil {
		return "", err
	}
	defer os.Remove(fname)

	old := binary + ".old"
	os.Remove(old)
	err = os.Rename(binary, old)
	if err != nil {
		return "", err
	}
	if err := os.Rename(fname, binary); err != nil {
		os.Rename(old, binary)
		return "", err
	}
	return rt, nil
}

func readRelease(archiveName, dir, url string) (string, string, error) {
	l.Debugf("loading %q", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}

	req.Header.Add("Accept", "application/octet-stream")
	resp, err := insecureHTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	switch path.Ext(archiveName) {
	case ".zip":
		return readZip(archiveName, dir, io.LimitReader(resp.Body, maxArchiveSize))
	default:
		return readTarGz(archiveName, dir, io.LimitReader(resp.Body, maxArchiveSize))
	}
}

func readTarGz(archiveName, dir string, r io.Reader) (string, string, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return "", "", err
	}

	tr := tar.NewReader(gr)

	var tempName string
	var sig []byte
	var comp []byte

	// Iterate through the files in the archive.
	i := 0
	for {
		if i >= maxArchiveMembers {
			break
		}
		i++

		hdr, err := tr.Next()
		if err == io.EOF {
			// end of tar archive
			break
		}
		if err != nil {
			return "", "", err
		}
		if hdr.Size > maxBinarySize {
			// We don't even want to try processing or skipping over files
			// that are too large.
			break
		}

		err = archiveFileVisitor(dir, &tempName, &sig, &comp, hdr.Name, tr)
		if err != nil {
			return "", "", err
		}

		if tempName != "" && sig != nil && comp != nil {
			break
		}
	}

	if err := verifyUpgrade(archiveName, tempName, sig, comp); err != nil {
		return "", "", err
	}

	var runtimeInfo RuntimeInfo
	err = json.Unmarshal(comp, &runtimeInfo)
	if err != nil {
		return "", "", err
	}

	return tempName, runtimeInfo.Runtime, nil
}

func readZip(archiveName, dir string, r io.Reader) (string, string, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", "", err
	}

	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", "", err
	}

	var tempName string
	var sig []byte
	var comp []byte

	// Iterate through the files in the archive.
	i := 0
	for _, file := range archive.File {
		if i >= maxArchiveMembers {
			break
		}
		i++

		if file.UncompressedSize64 > maxBinarySize {
			// We don't even want to try processing or skipping over files
			// that are too large.
			break
		}

		inFile, err := file.Open()
		if err != nil {
			return "", "", err
		}

		err = archiveFileVisitor(dir, &tempName, &sig, &comp, file.Name, inFile)
		inFile.Close()
		if err != nil {
			return "", "", err
		}

		if tempName != "" && sig != nil && comp != nil {
			break
		}
	}

	if err := verifyUpgrade(archiveName, tempName, sig, comp); err != nil {
		return "", "", err
	}

	var runtimeInfo RuntimeInfo
	err = json.Unmarshal(comp, &runtimeInfo)
	if err != nil {
		return "", "", err
	}

	return tempName, runtimeInfo.Runtime, nil
}

// archiveFileVisitor is called for each file in an archive. It may set
// tempFile and signature.
func archiveFileVisitor(dir string, tempFile *string, signature *[]byte, comp *[]byte, archivePath string, filedata io.Reader) error {
	var err error
	filename := path.Base(archivePath)
	archiveDir := path.Dir(archivePath)
	l.Debugf("considering file %s", archivePath)
	switch filename {
	case "syncthing", "syncthing.exe":
		archiveDirs := strings.Split(archiveDir, "/")
		if len(archiveDirs) > 1 {
			// Don't consider "syncthing" files found too deeply, as they may be
			// other things.
			return nil
		}
		l.Debugf("found upgrade binary %s", archivePath)
		*tempFile, err = writeBinary(dir, io.LimitReader(filedata, maxBinarySize))
		if err != nil {
			return err
		}

	case "release.sig":
		l.Debugf("found signature %s", archivePath)
		*signature, err = io.ReadAll(io.LimitReader(filedata, maxSignatureSize))
		if err != nil {
			return err
		}

	case CompatibilityJson:
		l.Debugf("found compatibility file %s", archivePath)
		*comp, err = io.ReadAll(io.LimitReader(filedata, maxCompatibilitySize))
		if err != nil {
			return err
		}
	}

	return nil
}

func verifyUpgrade(archiveName, tempName string, sig []byte, comp []byte) error {
	if tempName == "" {
		return errors.New("no upgrade found")
	}
	if sig == nil {
		return errors.New("no signature found")
	}
	if comp == nil {
		return errors.New(CompatibilityJson + " not found")
	}

	l.Debugf("checking signature\n%s", sig)

	fd, err := os.Open(tempName)
	if err != nil {
		return err
	}

	// Create a new reader that will serve reads from, in order:
	//
	// - the archive name ("syncthing-linux-amd64-v0.13.0-beta.4.tar.gz")
	//   followed by a newline
	//
	// - the temp file contents
	//
	// We then verify the release signature against the contents of this
	// multireader. This ensures that it is not only a bonafide syncthing
	// binary, but it is also of exactly the platform and version we expect.

	mr := io.MultiReader(strings.NewReader(archiveName+"\n"), fd)
	err = signature.Verify(SigningKey, sig, mr)
	fd.Close()

	if err != nil {
		os.Remove(tempName)
		return err
	}

	return nil
}

func verifyCompatibility(rel Release, rt string) error {
	l.Debugln("checking minimum OS version")

	majorMinor, err := normalizeRuntimeVersion(rt)
	if err != nil {
		return err
	}

	minOSVersion, ok := rel.MinOSVersions[majorMinor]
	if !ok {
		return fmt.Errorf("Go runtime %v not found", majorMinor)
	}

	currentKernel, err := host.KernelVersion()
	if err != nil {
		return err
	}
	currentKernel = normalizeKernelVersion(currentKernel)

	for hostArch, minKernel := range minOSVersion {
		host, arch, found := strings.Cut(hostArch, "/")
		if host != runtime.GOOS {
			continue
		}
		if found {
			if arch != runtime.GOARCH {
				continue
			}
		}
		if CompareVersions(minKernel, currentKernel) > Equal {
			return fmt.Errorf(unsupportedKernel, rt, minKernel, currentKernel)
		}
	}

	return nil
}

func writeBinary(dir string, inFile io.Reader) (filename string, err error) {
	// Write the binary to a temporary file.

	outFile, err := os.CreateTemp(dir, "syncthing")
	if err != nil {
		return "", err
	}

	_, err = io.Copy(outFile, inFile)
	if err != nil {
		os.Remove(outFile.Name())
		return "", err
	}

	err = outFile.Close()
	if err != nil {
		os.Remove(outFile.Name())
		return "", err
	}

	err = os.Chmod(outFile.Name(), os.FileMode(0755))
	if err != nil {
		os.Remove(outFile.Name())
		return "", err
	}

	return outFile.Name(), nil
}
