package mirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/s3"
)

// The bootstrap configuration is the one piece of Socrates' own state that is
// allowed to stay on the machine: it is what finds the bucket, so it cannot
// live in it. Everything else - the settings, the password, the sessions - is
// read from the bucket at startup.
//
// It comes from one of two places. A data directory connected through the
// setup page has <data>/s3.json, written by that page (mode 0600, because it
// holds the secret key and the passphrase). A service started with
// SOCRATES_S3_BUCKET in its environment takes everything from the environment
// instead and ignores the file: an operator who configures a deployment by
// its environment expects the environment to win, and a stale file left in
// the data directory must not quietly point the service somewhere else.

// ConfigFile is the name of the bootstrap file inside the data directory.
const ConfigFile = "s3.json"

// Source says where the configuration came from.
type Source string

const (
	// SourceNone: nothing is configured, and the server waits on the
	// connect page.
	SourceNone Source = ""
	// SourceFile: <data>/s3.json, written by the connect page.
	SourceFile Source = "file"
	// SourceEnv: the SOCRATES_S3_* environment. The admin page shows it
	// read-only, because a change there would be undone by the next start.
	SourceEnv Source = "env"
)

// Config is the bootstrap configuration: where the bucket is, how to sign
// for it, and the passphrase its contents are encrypted with.
type Config struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	PathStyle       bool   `json:"path_style"`
	Passphrase      string `json:"passphrase"`
}

// The environment variables. SOCRATES_S3_BUCKET is the switch: without it the
// others are ignored, so that a half-set environment is not mistaken for a
// configuration.
const (
	EnvEndpoint   = "SOCRATES_S3_ENDPOINT"
	EnvRegion     = "SOCRATES_S3_REGION"
	EnvBucket     = "SOCRATES_S3_BUCKET"
	EnvPrefix     = "SOCRATES_S3_PREFIX"
	EnvAccessKey  = "SOCRATES_S3_ACCESS_KEY_ID"
	EnvSecretKey  = "SOCRATES_S3_SECRET_ACCESS_KEY"
	EnvPathStyle  = "SOCRATES_S3_PATH_STYLE"
	EnvPassphrase = "SOCRATES_S3_PASSPHRASE"
)

// defaultRegion is what a configuration without a region signs for. It is
// AWS's default, and what most S3 compatible stores accept when they do not
// care.
const defaultRegion = "us-east-1"

// NormalizePrefix makes a prefix the one spelling the object keys are built
// from: "" for the bucket root, otherwise no leading slash and exactly one
// trailing one. "a/b", "/a/b/" and "a/b//" are all "a/b/".
func NormalizePrefix(prefix string) string {
	p := strings.Trim(strings.TrimSpace(prefix), "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// Normalize trims every field and applies the defaults. It is idempotent.
func (c *Config) Normalize() {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.Region = strings.TrimSpace(c.Region)
	if c.Region == "" {
		c.Region = defaultRegion
	}
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Prefix = NormalizePrefix(c.Prefix)
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	c.SecretAccessKey = strings.TrimSpace(c.SecretAccessKey)
	// The passphrase is not trimmed. A space at its end is unusual but it is
	// the person's, and trimming it would derive a key nobody can reproduce
	// from what they typed.
}

// Validate says what is missing, in a sentence the connect page can show.
func (c Config) Validate() error {
	switch {
	case c.Bucket == "":
		return errors.New("the bucket name is missing")
	case c.AccessKeyID == "" || c.SecretAccessKey == "":
		return errors.New("the access key id and the secret access key are both needed")
	case c.Passphrase == "":
		return errors.New("the encryption passphrase is missing")
	}
	_, err := s3.New(c.S3())
	return err
}

// S3 is the client configuration of this bootstrap configuration.
func (c Config) S3() s3.Config {
	return s3.Config{
		Endpoint:        c.Endpoint,
		Region:          c.Region,
		Bucket:          c.Bucket,
		PathStyle:       c.PathStyle,
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
	}
}

// Identity names the place the state lives: endpoint, bucket and prefix. It is
// what a local copy remembers it was last synchronised with, and what decides
// whether a server that cannot reach its bucket may start from that copy - a
// configuration that now points somewhere else must not.
func (c Config) Identity() string {
	endpoint := strings.ToLower(strings.TrimRight(strings.TrimSpace(c.Endpoint), "/"))
	if endpoint != "" && !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	return endpoint + "|" + c.Bucket + "|" + NormalizePrefix(c.Prefix)
}

// SameBucket reports whether two configurations name the same bucket and
// prefix, whatever the credentials and the endpoint spelling.
func (c Config) SameBucket(o Config) bool {
	return c.Bucket == o.Bucket && NormalizePrefix(c.Prefix) == NormalizePrefix(o.Prefix)
}

// Public is the configuration without its two secrets, for status pages.
type Public struct {
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	AccessKeyID string `json:"access_key_id"`
	PathStyle   bool   `json:"path_style"`
	Source      Source `json:"source"`
}

// Public strips the secret key and the passphrase.
func (c Config) Public(src Source) Public {
	return Public{
		Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, Prefix: c.Prefix,
		AccessKeyID: c.AccessKeyID, PathStyle: c.PathStyle, Source: src,
	}
}

// ConfigPath is where the bootstrap file of a data directory is.
func ConfigPath(dataDir string) string { return filepath.Join(dataDir, ConfigFile) }

// LoadConfig reads the bootstrap configuration: the environment when
// SOCRATES_S3_BUCKET is set, otherwise <data>/s3.json, otherwise nothing
// (SourceNone and a nil error). A file that is there but unreadable is an
// error rather than "nothing": treating it as unconfigured would offer the
// connect page, and with it a way to point a configured machine at another
// bucket.
func LoadConfig(dataDir string) (Config, Source, error) {
	return loadConfig(dataDir, os.Getenv)
}

func loadConfig(dataDir string, getenv func(string) string) (Config, Source, error) {
	if strings.TrimSpace(getenv(EnvBucket)) != "" {
		cfg := Config{
			Endpoint:        getenv(EnvEndpoint),
			Region:          getenv(EnvRegion),
			Bucket:          getenv(EnvBucket),
			Prefix:          getenv(EnvPrefix),
			AccessKeyID:     getenv(EnvAccessKey),
			SecretAccessKey: getenv(EnvSecretKey),
			PathStyle:       true,
			Passphrase:      getenv(EnvPassphrase),
		}
		if v := strings.TrimSpace(getenv(EnvPathStyle)); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return Config{}, SourceEnv, fmt.Errorf("%s=%q is neither true nor false", EnvPathStyle, v)
			}
			cfg.PathStyle = b
		}
		cfg.Normalize()
		return cfg, SourceEnv, nil
	}
	raw, err := os.ReadFile(ConfigPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, SourceNone, nil
	}
	if err != nil {
		return Config{}, SourceFile, fmt.Errorf("read %s: %w", ConfigPath(dataDir), err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, SourceFile, fmt.Errorf("%s is not valid JSON: %w", ConfigPath(dataDir), err)
	}
	cfg.Normalize()
	return cfg, SourceFile, nil
}

// SaveConfig writes <data>/s3.json, mode 0600, through a temporary file and
// a rename, so a crash half way leaves the old file or the new one and never
// a truncated one - a truncated bootstrap file is a server that cannot find
// its state.
func SaveConfig(dataDir string, cfg Config) error {
	cfg.Normalize()
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".s3-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), ConfigPath(dataDir))
}
