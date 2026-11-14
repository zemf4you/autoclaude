// Package desktop sets Claude Desktop up for the model menu of the gateway.
//
// Two things are needed. The user's account has to trust the certificate the
// gateway presents as claude.ai, and Desktop has to send its connections to
// claude.ai through the gateway. For the second, Desktop has a documented
// setting, egressProxyPacUrl, that every platform stores in its own place.
//
// Everything goes through the tools of the operating system and is scoped as
// narrowly as the platform allows: the certificate to the current user, the
// proxy setting to Claude Desktop.
package desktop

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// proxySetting is the Claude Desktop setting that names a proxy auto-config
// script for the app's own traffic.
const proxySetting = "egressProxyPacUrl"

// authority is the certificate authority the user's account has to trust.
type authority struct {
	path string // PEM file
	name string // common name
	sha1 string // fingerprint, the way trust stores identify a certificate
}

// Enable makes the user's account trust the certificate authority at
// certPath and points Claude Desktop at the proxy auto-config script at
// pacURL. It is safe to repeat.
func Enable(certPath, pacURL string) error {
	ca, err := readAuthority(certPath)
	if err != nil {
		return err
	}
	if !trusted(ca) {
		if err := trust(ca); err != nil {
			return err
		}
	}
	return setProxy(pacURL)
}

// Disable undoes Enable. It is safe to call when nothing was enabled.
func Disable(certPath string) error {
	if err := unsetProxy(); err != nil {
		return err
	}
	ca, err := readAuthority(certPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // the authority was never created, so never trusted
	}
	if err != nil {
		return err
	}
	if !trusted(ca) {
		return nil
	}
	return untrust(ca)
}

// Launch starts Claude Desktop with the proxy auto-config script given on
// its command line. It is the fallback for machines where the proxy setting
// cannot be installed, such as those whose Claude Desktop is already managed
// by an organization. The script travels inline, as a data URL.
func Launch(pacScript string) error {
	return launch("--proxy-pac-url=data:application/x-ns-proxy-autoconfig," + url.PathEscape(pacScript))
}

func readAuthority(certPath string) (authority, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return authority{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return authority{}, fmt.Errorf("%s: not a PEM certificate", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return authority{}, fmt.Errorf("%s: %w", certPath, err)
	}
	return authority{
		path: certPath,
		name: cert.Subject.CommonName,
		sha1: fmt.Sprintf("%X", sha1.Sum(cert.Raw)),
	}, nil
}

// run executes a command, letting it talk to the user: trust stores and
// sudo ask for a password or a confirmation.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// succeeds reports whether a command exits with success, discarding its output.
func succeeds(name string, args ...string) bool {
	return exec.Command(name, args...).Run() == nil
}
