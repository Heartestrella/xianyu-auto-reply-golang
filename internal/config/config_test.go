package config

import "testing"

func TestMessageAPIHasNoDefaultKey(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("JWT_SECRET", "test-jwt")
	for _, key := range []string{"", "  ", " configured-secret "} {
		t.Setenv("API_SECRET_KEY", key)
		want := ""
		if key == " configured-secret " {
			want = "configured-secret"
		}
		if got := Load().APISecretKey; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}
