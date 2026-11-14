package desktop

// policyKey is where Claude Desktop reads the settings of the current user.
const policyKey = `HKCU\SOFTWARE\Policies\Claude`

func trusted(ca authority) bool {
	return succeeds("certutil", "-user", "-verifystore", "Root", ca.sha1)
}

func trust(ca authority) error {
	return run("certutil", "-user", "-addstore", "Root", ca.path)
}

func untrust(ca authority) error {
	return run("certutil", "-user", "-delstore", "Root", ca.sha1)
}

func setProxy(pacURL string) error {
	return run("reg", "add", policyKey, "/v", proxySetting, "/t", "REG_SZ", "/d", pacURL, "/f")
}

func unsetProxy() error {
	if !succeeds("reg", "query", policyKey, "/v", proxySetting) {
		return nil
	}
	return run("reg", "delete", policyKey, "/v", proxySetting, "/f")
}

func launch(flag string) error {
	return run("cmd", "/c", "start", "", "claude", flag)
}
