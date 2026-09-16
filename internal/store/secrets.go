package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"ctlvps/internal/auth"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const secretPrefix = "enc:v1:"

func loadSecretKey(path string) (cipher.AEAD, error) {
	var key []byte
	if path == "" {
		key = make([]byte, 32)
		if _, e := rand.Read(key); e != nil {
			return nil, e
		}
	} else {
		st, e := os.Lstat(path)
		if os.IsNotExist(e) {
			if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				return nil, e
			}
			key = make([]byte, 32)
			if _, e = rand.Read(key); e != nil {
				return nil, e
			}
			f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return nil, e
			}
			if _, e = f.Write(key); e == nil {
				e = f.Sync()
			}
			ce := f.Close()
			if e != nil {
				return nil, e
			}
			if ce != nil {
				return nil, ce
			}
		} else if e != nil {
			return nil, e
		} else {
			if !st.Mode().IsRegular() || st.Mode().Perm()&0007 != 0 || st.Mode().Perm()&0020 != 0 {
				return nil, errors.New("数据密钥文件权限不安全")
			}
			key, e = os.ReadFile(path)
			if e != nil {
				return nil, e
			}
		}
	}
	if len(key) != 32 {
		return nil, errors.New("数据密钥长度无效")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}

type secretValue struct {
	s            *Store
	label, value string
}

func (v secretValue) Value() (driver.Value, error)      { return v.s.encrypt(v.label, v.value) }
func (s *Store) seal(label, value string) driver.Valuer { return secretValue{s, label, value} }
func (s *Store) encrypt(label, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	nonce := make([]byte, s.secret.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return "", e
	}
	b := s.secret.Seal(nonce, nonce, []byte(value), []byte(label))
	return secretPrefix + base64.RawStdEncoding.EncodeToString(b), nil
}
func (s *Store) decrypt(label, value string) (string, error) {
	if !strings.HasPrefix(value, secretPrefix) {
		return value, nil
	}
	b, e := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, secretPrefix))
	n := s.secret.NonceSize()
	if e != nil || len(b) < n {
		return "", errors.New("敏感字段损坏")
	}
	p, e := s.secret.Open(nil, b[:n], b[n:], []byte(label))
	if e != nil {
		return "", errors.New("数据密钥不匹配或敏感字段损坏")
	}
	return string(p), nil
}

type secretScanner struct {
	s      *Store
	label  string
	target *string
}

func (v secretScanner) Scan(src any) error {
	var raw string
	switch x := src.(type) {
	case string:
		raw = x
	case []byte:
		raw = string(x)
	case nil:
	default:
		return errors.New("invalid secret column")
	}
	p, e := v.s.decrypt(v.label, raw)
	if e == nil {
		*v.target = p
	}
	return e
}
func (s *Store) scanSecret(label string, p *string) secretScanner { return secretScanner{s, label, p} }

// migrateSecrets upgrades legacy plaintext in one transaction before the API is
// exposed. Labels prevent ciphertext from being substituted across columns.
func (s *Store) migrateSecrets() error {
	columns := map[string][]string{"nodes": {"params", "server_params"}, "subscriptions": {"token", "short_code"}, "users": {"totp_secret"}, "external_subscriptions": {"url", "raw_content"}, "desired_states": {"payload"}, "settings": {"value"}, "maintenance_jobs": {"report_token"}, "rule_templates": {"content", "variables"}, "proxy_group_presets": {"groups_json", "rules_json"}}
	tx, e := s.db.BeginTx(context.Background(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for table, cols := range columns {
		for _, col := range cols {
			rows, e := tx.Query("SELECT rowid," + col + " FROM " + table)
			if e != nil {
				return e
			}
			type item struct {
				id    int64
				value string
			}
			var pending []item
			for rows.Next() {
				var i item
				if e = rows.Scan(&i.id, &i.value); e != nil {
					rows.Close()
					return e
				}
				if _, e = s.decrypt(table+"."+col, i.value); e != nil {
					rows.Close()
					return e
				}
				if i.value != "" && !strings.HasPrefix(i.value, secretPrefix) {
					pending = append(pending, i)
					s.migratedSecrets = true
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, i := range pending {
				if _, e = tx.Exec("UPDATE "+table+" SET "+col+"=? WHERE rowid=?", s.seal(table+"."+col, i.value), i.id); e != nil {
					return e
				}
			}
		}
	}
	rows, e := tx.Query("SELECT id,short_code FROM subscriptions WHERE short_code_hash='' AND short_code!=''")
	if e != nil {
		return e
	}
	type subCode struct {
		id  int64
		raw string
	}
	var codes []subCode
	for rows.Next() {
		var c subCode
		if e = rows.Scan(&c.id, &c.raw); e != nil {
			rows.Close()
			return e
		}
		codes = append(codes, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, c := range codes {
		plain, e := s.decrypt("subscriptions.short_code", c.raw)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE subscriptions SET short_code_hash=? WHERE id=?", auth.HashToken(plain), c.id); e != nil {
			return e
		}
	}
	return tx.Commit()
}
