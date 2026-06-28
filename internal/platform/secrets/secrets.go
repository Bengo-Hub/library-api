// Package secrets provides platform-configurable, encrypted-at-rest credential storage for
// library-api, mirroring the treasury-api pattern (credential-encryption-key-config): the
// AES-256-GCM key is resolved DB-first (platform ServiceConfig row "encryption_key") → env
// (LIBRARY_ENCRYPTION_KEY) → deterministic dev key, and decryption tries every candidate in
// order so rotating the key never orphans existing data. Integration secrets (e.g. the ISBNdb
// API key) live in platform-level ServiceConfig rows, encrypted with the PRIMARY key. Raw key
// material and secret values are NEVER logged or returned in API responses — only a sha256
// fingerprint + source.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/library-service/internal/ent"
	"github.com/bengobox/library-service/internal/ent/serviceconfig"
)

const (
	// EncryptionKeyConfigKey is the platform ServiceConfig.config_key holding the base64-std of
	// the 32-byte AES key (tenant_id NULL).
	EncryptionKeyConfigKey = "encryption_key"
	// KeyISBNdbAPIKey is the platform ServiceConfig.config_key holding the (encrypted) ISBNdb API key.
	KeyISBNdbAPIKey = "isbndb_api_key"
	// KeyISBNdbBaseURL is the platform ServiceConfig.config_key holding the ISBNdb API base URL
	// (plaintext, not secret). Differs by plan: api2 (Basic) / api.premium / api.pro.
	KeyISBNdbBaseURL = "isbndb_base_url"
	// ISBNdbDefaultBaseURL is the Basic-plan host, used when no base URL is configured.
	ISBNdbDefaultBaseURL = "https://api2.isbndb.com"

	envKeyVar     = "LIBRARY_ENCRYPTION_KEY"
	devKey        = "bengobox-library-dev-key-32byte!" // 32 bytes; LAST candidate so dev data still decrypts
	candidatesTTL = 30 * time.Second
)

type keySource string

const (
	keySourceDB   keySource = "db"
	keySourceEnv  keySource = "env"
	keySourceDev  keySource = "dev"
	keySourceNone keySource = ""
)

// KeyStatus is the key-free, externally safe description of the active PRIMARY key.
type KeyStatus struct {
	Configured  bool
	Source      string
	Fingerprint string
	DBUpdatedAt *time.Time
}

type keyCandidates struct {
	keys          [][]byte
	primarySource keySource
	dbUpdatedAt   *time.Time
}

// KeyProvider resolves ordered candidate AES-256 keys with a short TTL cache (concurrency-safe).
type KeyProvider struct {
	client *ent.Client
	log    *zap.Logger

	mu       sync.RWMutex
	cached   *keyCandidates
	cachedAt time.Time
}

func newKeyProvider(client *ent.Client, log *zap.Logger) *KeyProvider {
	if log == nil {
		log = zap.NewNop()
	}
	return &KeyProvider{client: client, log: log}
}

// Invalidate clears the cache so the next resolution re-reads the DB (call after a key change).
func (p *KeyProvider) Invalidate() {
	p.mu.Lock()
	p.cached = nil
	p.cachedAt = time.Time{}
	p.mu.Unlock()
}

func (p *KeyProvider) candidates(ctx context.Context) *keyCandidates {
	p.mu.RLock()
	if p.cached != nil && time.Since(p.cachedAt) < candidatesTTL {
		c := p.cached
		p.mu.RUnlock()
		return c
	}
	p.mu.RUnlock()

	resolved := p.resolve(ctx)
	p.mu.Lock()
	p.cached = resolved
	p.cachedAt = time.Now()
	p.mu.Unlock()
	return resolved
}

// Status reports the active PRIMARY key's source + fingerprint without exposing key material.
func (p *KeyProvider) Status(ctx context.Context) KeyStatus {
	c := p.candidates(ctx)
	return KeyStatus{
		Configured:  c.primarySource == keySourceDB,
		Source:      string(c.primarySource),
		Fingerprint: fingerprint(c.keys[0]),
		DBUpdatedAt: c.dbUpdatedAt,
	}
}

func (p *KeyProvider) resolve(ctx context.Context) *keyCandidates {
	out := &keyCandidates{primarySource: keySourceNone}
	seen := make(map[string]struct{}, 3)
	add := func(key []byte, src keySource, updatedAt *time.Time) {
		if len(key) != 32 {
			return
		}
		if _, dup := seen[string(key)]; dup {
			return
		}
		seen[string(key)] = struct{}{}
		if len(out.keys) == 0 {
			out.primarySource = src
			out.dbUpdatedAt = updatedAt
		}
		out.keys = append(out.keys, key)
	}
	if dbKey, updatedAt := p.loadDBKey(ctx); dbKey != nil {
		add(dbKey, keySourceDB, updatedAt)
	}
	if envKey := loadEnvKey(); envKey != nil {
		add(envKey, keySourceEnv, nil)
	}
	add(normalizeRawKey(devKey), keySourceDev, nil)
	return out
}

func (p *KeyProvider) loadDBKey(ctx context.Context) ([]byte, *time.Time) {
	if p.client == nil {
		return nil, nil
	}
	row, err := p.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(EncryptionKeyConfigKey), serviceconfig.TenantIDIsNil()).
		First(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			p.log.Warn("failed to load DB encryption key, falling back", zap.Error(err))
		}
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(row.ConfigValue)
	if err != nil || len(decoded) != 32 {
		p.log.Warn("DB encryption key is not valid base64 of 32 bytes, ignoring")
		return nil, nil
	}
	updatedAt := row.UpdatedAt
	return decoded, &updatedAt
}

func loadEnvKey() []byte {
	keyStr := os.Getenv(envKeyVar)
	if keyStr == "" {
		return nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(keyStr); err == nil && len(decoded) == 32 {
		return decoded
	}
	return normalizeRawKey(keyStr)
}

func normalizeRawKey(s string) []byte {
	raw := []byte(s)
	if len(raw) >= 32 {
		return raw[:32]
	}
	padded := make([]byte, 32)
	copy(padded, raw)
	return padded
}

func fingerprint(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:8]
}

// encrypt seals plaintext with AES-256-GCM under the PRIMARY key (base64-std output).
func (p *KeyProvider) encrypt(plaintext string) (string, error) {
	key := p.candidates(context.Background()).keys[0]
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt opens an AES-256-GCM string, trying each candidate key (DB → env → dev) so data
// encrypted under an older key still decrypts. Falls back to plaintext passthrough (dev/migration).
func (p *KeyProvider) decrypt(ctx context.Context, encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return encrypted, nil // not base64 → treat as legacy plaintext
	}
	for _, key := range p.candidates(ctx).keys {
		block, err := aes.NewCipher(key)
		if err != nil {
			continue
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			continue
		}
		if len(data) < gcm.NonceSize() {
			break
		}
		nonce, ct := data[:gcm.NonceSize()], data[gcm.NonceSize():]
		if plaintext, err := gcm.Open(nil, nonce, ct, nil); err == nil {
			return string(plaintext), nil
		}
	}
	// All keys failed → the base64 blob may itself be legacy plaintext.
	return string(data), nil
}

// Store is the platform-level encrypted config store over ServiceConfig (tenant_id NULL rows).
type Store struct {
	client *ent.Client
	kp     *KeyProvider
	log    *zap.Logger
}

// NewStore builds a Store (and its KeyProvider) over the ent client.
func NewStore(client *ent.Client, log *zap.Logger) *Store {
	if log == nil {
		log = zap.NewNop()
	}
	return &Store{client: client, kp: newKeyProvider(client, log), log: log}
}

// KeyProvider exposes the underlying provider so the platform handler can invalidate after a change.
func (s *Store) KeyProvider() *KeyProvider { return s.kp }

// EncryptionStatus reports the active encryption key (masked).
func (s *Store) EncryptionStatus(ctx context.Context) KeyStatus { return s.kp.Status(ctx) }

// SetEncryptionKey persists the platform AES key (raw 32 bytes, stored base64) and invalidates cache.
func (s *Store) SetEncryptionKey(ctx context.Context, keyBytes []byte) error {
	if len(keyBytes) != 32 {
		return errors.New("key must be exactly 32 bytes")
	}
	encoded := base64.StdEncoding.EncodeToString(keyBytes)
	if err := s.upsert(ctx, EncryptionKeyConfigKey, encoded, "AES-256-GCM key for credential encryption at rest", true); err != nil {
		return err
	}
	s.kp.Invalidate()
	return nil
}

// GetSecret returns the decrypted value for a platform secret key, ok=false when unset/empty.
func (s *Store) GetSecret(ctx context.Context, key string) (string, bool) {
	row, err := s.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(key), serviceconfig.TenantIDIsNil()).First(ctx)
	if err != nil {
		return "", false
	}
	val, err := s.kp.decrypt(ctx, row.ConfigValue)
	if err != nil || val == "" {
		return "", false
	}
	return val, true
}

// SetSecret encrypts and upserts a platform secret value (empty value clears the row).
func (s *Store) SetSecret(ctx context.Context, key, plaintext, description string) error {
	if plaintext == "" {
		_, err := s.client.ServiceConfig.Delete().
			Where(serviceconfig.ConfigKey(key), serviceconfig.TenantIDIsNil()).Exec(ctx)
		if ent.IsNotFound(err) {
			return nil
		}
		return err
	}
	enc, err := s.kp.encrypt(plaintext)
	if err != nil {
		return err
	}
	return s.upsert(ctx, key, enc, description, true)
}

// GetConfig returns a non-secret plaintext config value, ok=false when unset/empty.
func (s *Store) GetConfig(ctx context.Context, key string) (string, bool) {
	row, err := s.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(key), serviceconfig.TenantIDIsNil()).First(ctx)
	if err != nil || row.ConfigValue == "" {
		return "", false
	}
	return row.ConfigValue, true
}

// SetConfig upserts a non-secret plaintext config value (empty clears the row).
func (s *Store) SetConfig(ctx context.Context, key, value, description string) error {
	if value == "" {
		_, err := s.client.ServiceConfig.Delete().
			Where(serviceconfig.ConfigKey(key), serviceconfig.TenantIDIsNil()).Exec(ctx)
		if ent.IsNotFound(err) {
			return nil
		}
		return err
	}
	return s.upsert(ctx, key, value, description, false)
}

// SecretStatus reports whether a platform secret is configured + a safe fingerprint of its value.
func (s *Store) SecretStatus(ctx context.Context, key string) (configured bool, fp string, updatedAt *time.Time) {
	row, err := s.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(key), serviceconfig.TenantIDIsNil()).First(ctx)
	if err != nil {
		return false, "", nil
	}
	val, derr := s.kp.decrypt(ctx, row.ConfigValue)
	if derr != nil || val == "" {
		return false, "", nil
	}
	ts := row.UpdatedAt
	return true, fingerprint([]byte(val)), &ts
}

func (s *Store) upsert(ctx context.Context, key, value, description string, isSecret bool) error {
	existing, err := s.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(key), serviceconfig.TenantIDIsNil()).First(ctx)
	if err != nil {
		_, createErr := s.client.ServiceConfig.Create().
			SetConfigKey(key).SetConfigValue(value).SetConfigType("string").
			SetIsSecret(isSecret).SetDescription(description).Save(ctx)
		return createErr
	}
	_, updErr := existing.Update().
		SetConfigValue(value).SetConfigType("string").
		SetIsSecret(isSecret).SetDescription(description).Save(ctx)
	return updErr
}
