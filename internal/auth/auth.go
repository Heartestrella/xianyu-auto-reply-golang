package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"strconv"
	"strings"
	"time"
)

type Claims struct {
	Sub      int64  `json:"sub"`
	Username string `json:"username"`
	Admin    bool   `json:"is_admin"`
	Type     string `json:"type"`
	Exp      int64  `json:"exp"`
}

func Hash(s string) (string, error) {
	b, e := bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
	return string(b), e
}
func Verify(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
func Sign(c Claims, secret string, ttl time.Duration) string {
	c.Exp = time.Now().Add(ttl).Unix()
	h := enc([]byte(`{"alg":"HS256","typ":"JWT"}`))
	b, _ := json.Marshal(c)
	p := h + "." + enc(b)
	return p + "." + signature(p, secret)
}
func Parse(token, secret string) (Claims, error) {
	var c Claims
	p := strings.Split(token, ".")
	if len(p) != 3 || !hmac.Equal([]byte(p[2]), []byte(signature(p[0]+"."+p[1], secret))) {
		return c, errors.New("invalid token")
	}
	b, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if c.Exp < time.Now().Unix() {
		return c, errors.New("expired token")
	}
	return c, nil
}
func Bearer(v string) (string, error) {
	p := strings.SplitN(v, " ", 2)
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") {
		return "", errors.New("missing bearer token")
	}
	return p[1], nil
}
func enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func signature(p, s string) string {
	m := hmac.New(sha256.New, []byte(s))
	m.Write([]byte(p))
	return enc(m.Sum(nil))
}
func Subject(c Claims) string { return strconv.FormatInt(c.Sub, 10) }
func Error(e error) error     { return fmt.Errorf("认证失败: %w", e) }
