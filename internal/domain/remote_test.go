package domain

import "testing"

func TestValidRemotePlatformAllowsPhonesAndDesktop(t *testing.T) {
	for _, platform := range []string{RemotePlatformIOS, RemotePlatformAndroid, RemotePlatformDesktop} {
		if !ValidRemotePlatform(platform) {
			t.Fatalf("ValidRemotePlatform(%q) = false", platform)
		}
	}
	if ValidRemotePlatform("windows") || ValidRemotePlatform("") {
		t.Fatal("unexpected platform accepted")
	}
}
