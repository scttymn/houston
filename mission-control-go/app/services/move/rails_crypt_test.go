package move

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The Rails app's development keys (mission_control/config/environments/
// development.rb): testdata/rails-encrypted.json is what that app's
// ActiveRecord::Encryption.encryptor made of each value with them.
var devKeys = RailsKeys{
	PrimaryKey: "dev-only-primary-key-not-for-production-use",
	Salt:       "dev-only-key-derivation-salt-not-for-production",
}

type vector struct{ Name, Plain, Stored string }

func vectors(t *testing.T) []vector {
	t.Helper()
	data, err := os.ReadFile("testdata/rails-encrypted.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(data, &vs); err != nil {
		t.Fatal(err)
	}
	return vs
}

// What Rails encrypted decrypts to what went in: short, empty and unicode
// values, a long one Rails compressed first, and a JSON document (a storage
// location's credentials).
func TestDecrypt(t *testing.T) {
	c, err := NewRailsCipher(devKeys)
	if err != nil {
		t.Fatal(err)
	}
	vs := vectors(t)
	if len(vs) != 5 || !strings.Contains(vs[3].Stored, `"c":true`) {
		t.Fatalf("the vectors: %d, the long one compressed? %s", len(vs), vs[3].Stored)
	}
	for _, v := range vs {
		got, err := c.Decrypt(v.Stored)
		if err != nil || got != v.Plain {
			t.Errorf("%s: %q, %v; want %q", v.Name, got, err, v.Plain)
		}
	}
}

// A wrong key, a changed byte or a value that isn't Rails' envelope is an
// error, never garbage passed on as a secret.
func TestDecryptRefuses(t *testing.T) {
	good, _ := NewRailsCipher(devKeys)
	wrong, _ := NewRailsCipher(RailsKeys{PrimaryKey: "another-primary-key", Salt: devKeys.Salt})
	v := vectors(t)[0]
	if _, err := wrong.Decrypt(v.Stored); !errors.Is(err, ErrNotDecrypted) {
		t.Errorf("a wrong key: %v", err)
	}
	var env map[string]any
	json.Unmarshal([]byte(v.Stored), &env)
	env["p"] = "M1SIG1uPeA=="
	changed, _ := json.Marshal(env)
	if _, err := good.Decrypt(string(changed)); !errors.Is(err, ErrNotDecrypted) {
		t.Errorf("a changed byte: %v", err)
	}
	for _, bad := range []string{"hunter2", "", `{"p":"x"}`, `{"p":"AA==","h":{"iv":"!!","at":"AA=="}}`,
		`{"p":"AA==","h":{"iv":"AAAA","at":"AAAAAAAAAAAAAAAAAAAAAA=="}}`} {
		if _, err := good.Decrypt(bad); !errors.Is(err, ErrNotDecrypted) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := NewRailsCipher(RailsKeys{}); err == nil {
		t.Error("no keys")
	}
}
