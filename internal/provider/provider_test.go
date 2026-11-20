package provider

import "testing"

func TestGetGoogle(t *testing.T) {
	spec, err := Get("google")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "google" {
		t.Errorf("name = %q, want google", spec.Name)
	}
	if spec.Endpoint.AuthURL == "" || spec.Endpoint.TokenURL == "" {
		t.Errorf("endpoint incomplete: %+v", spec.Endpoint)
	}
	if len(spec.Scopes) == 0 {
		t.Error("want at least one scope")
	}
}

func TestGetUnknown(t *testing.T) {
	if _, err := Get("dropbox"); err == nil {
		t.Error("want error for unknown provider, got nil")
	}
}

func TestNames(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("want at least one provider")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("names not sorted: %v", names)
		}
	}
}
