package main

import "testing"

func TestValidatePublicURL(t *testing.T) {
	for _, value := range []string{"https://substrate.orb.local", "https://platform.test/", "https://platform.test:8443"} {
		if err := validatePublicURL(value); err != nil {
			t.Errorf("valid origin %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "http://platform.test", "https://", "https:///platform.test", "https://user:password@platform.test", "https://platform.test/workbench", "https://platform.test?x=1", "https://platform.test#x", "https://platform.test:0", "https://platform.test:65536"} {
		if err := validatePublicURL(value); err == nil {
			t.Errorf("invalid public origin %q accepted", value)
		}
	}
}
