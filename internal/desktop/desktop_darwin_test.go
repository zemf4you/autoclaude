package desktop

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestProfile(t *testing.T) {
	var out bytes.Buffer
	settings := struct{ ID, Name, Setting, PACURL string }{profileID, profileName, proxySetting, "http://127.0.0.1:8484/proxy.pac?a=1&b=2"}
	if err := profile.Execute(&out, settings); err != nil {
		t.Fatal(err)
	}
	want := "<key>egressProxyPacUrl</key><string>http://127.0.0.1:8484/proxy.pac?a=1&amp;b=2</string>"
	if !strings.Contains(out.String(), want) {
		t.Errorf("profile lacks %s:\n%s", want, out.String())
	}
	if err := xml.Unmarshal(out.Bytes(), new(any)); err != nil {
		t.Errorf("profile is not well-formed XML: %v", err)
	}
}
