package nodeconfig

import "testing"

func TestValidateLoopbackAcceptsLocalAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7462", "localhost:7462", "[::1]:7462"} {
		if err := ValidateLoopback(address); err != nil {
			t.Errorf("ValidateLoopback(%q) error = %v", address, err)
		}
	}
}

func TestValidateLoopbackRejectsLANBindingUntilAuthExists(t *testing.T) {
	for _, address := range []string{"0.0.0.0:7462", "192.168.1.10:7462", ":7462"} {
		if err := ValidateLoopback(address); err == nil {
			t.Errorf("ValidateLoopback(%q) succeeded; want rejection", address)
		}
	}
}
